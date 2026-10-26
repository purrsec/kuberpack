package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/fail"
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/netguard"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// deliveryNonce returns a short random suffix for a delivery id. An explicit
// user action (register, redeploy) must always queue a new build, even for a
// commit that was already built: the delivery id is the primary key of
// webhook_deliveries, so a repeated one would be dropped as a duplicate.
// A randomness failure is an error: falling back to the clock would make ids
// predictable.
func deliveryNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate delivery nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

const maxBody = 1 << 20

type Config struct {
	Builder builder.Runner
	Store   *store.Store
	Secrets store.SecretsDir
	// Provider is the git forge Kuberpack talks to. It is the only thing
	// that knows whether the world is a Forgejo, a GitHub or a GitLab.
	Provider git.Provider
	// RegistryURL is the OCI registry the images are pushed to. It is
	// independent of the forge: GitHub pushes to ghcr.io, GitLab to its own
	// registry, a Forgejo to its own port.
	RegistryURL string
	// RegistryUser is the namespace the images are published in. It is the
	// account of the platform, not the owner of the application
	// repository: the code of a third party application is built and
	// published in the namespace of the platform.
	RegistryUser  string
	RegistryToken string
	Token         string
	APIToken      string
	WebhookURL    string
	GitOpsURL     string
	GitOpsBranch  string
	// AppsSource is the Flux GitRepository that carries the applications
	// repository. With tenant isolation, each project gets its own
	// Kustomization that references it.
	AppsSource string
	// TenantIsolation makes the control plane provision one namespace,
	// ServiceAccount and Flux Kustomization per project. The application
	// Kustomization then applies with the tenant identity, not cluster-admin.
	TenantIsolation bool
	// Reloader reports whether the secret reloader is installed. The
	// provisioner then binds its identity in each project namespace.
	Reloader bool
	// ChartPath is the stateless chart on the disk of the control plane.
	// Rendering never depends on a chart the control plane could rewrite.
	ChartPath string
	// WriteCharts keeps the historical behaviour where the control plane
	// publishes its chart into the repository it writes. A split platform
	// sets it to false: the platform repository is installer-owned.
	WriteCharts bool
	// SelfChartPath is the chart of the control plane itself, on the disk
	// of the control plane. The platform writes it in the GitOps
	// repository: the deployment of the platform must never drift from
	// the image that runs it.
	SelfChartPath string
	// SelfChartRef is where the chart of the control plane lives in the
	// GitOps repository, the way the HelmRelease references it.
	SelfChartRef string
	Platform     promote.Platform
	Wait         time.Duration
	PreviewTTL   time.Duration
	// ReconcileInterval is how often the manifests of the GitOps repository
	// are read again. A webhook can be lost: a forge that drops a delivery,
	// a control plane that restarts at the wrong moment. The repository is
	// the truth, so the state is reconciled from it.
	ReconcileInterval time.Duration
	BuildsGitURL      string
	FluxSecret        string
	Run               func(context.Context, release.Request) (release.Result, error)
	GitOpsFile        func(context.Context, store.App) ([]byte, error)
	// Kube is the cluster the applications live in. The platform writes
	// there what must not be written in the GitOps repository: the secret
	// that pulls the images of the registry.
	Kube        kubernetes.Interface
	KubeDynamic dynamic.Interface
	// KubeConfig is the configuration of the cluster. The platform needs it
	// to reach the resources that have no Go type, the releases of Flux.
	KubeConfig *rest.Config
	// KubeletCA optionally supplies the PEM serving CA used by kubelets when
	// it differs from the API server CA. Certificate verification stays enabled.
	KubeletCA []byte
	// BuilderNamespace is the namespace where the build Jobs run. It is
	// where the logs of a build are.
	BuilderNamespace string
	// BuilderCacheName is the volume that holds the cache of the builder:
	// the database of the vulnerabilities of Trivy.
	BuilderCacheName string
	// BuilderCacheSize is the size of that volume.
	BuilderCacheSize         string
	BuilderCacheStorageClass string
	// Egress garde les accès sortants déclenchés par un manifeste : clone
	// d'un dépôt, lecture d'un registre. Nil retombe sur la politique de
	// l'environnement.
	Egress *netguard.Policy
	// EgressAllow est la forme texte de l'allowlist, transmise au builder.
	EgressAllow string
	// APIRateLimit est le nombre de requêtes par minute et par client. Zéro
	// prend la valeur par défaut, une valeur négative désactive la limite.
	APIRateLimit int
	// APIRateBurst est la taille de la rafale autorisée. Zéro prend un quart
	// de APIRateLimit.
	APIRateBurst int
	// APIMaxConcurrent borne les requêtes simultanées. Zéro prend la valeur
	// par défaut, une valeur négative désactive la borne.
	APIMaxConcurrent int
	// TrustedProxies sont les CIDR des proxys dont X-Forwarded-For est lu.
	// Vide : l'en-tête est ignoré et seule l'adresse de connexion compte.
	TrustedProxies []string
}

type Server struct {
	planLimiter  *apiLimiter
	deploymentMu sync.Mutex
	cfg          Config
	wake         chan struct{}
	logFollowers chan struct{}
	limiter      *apiLimiter
	trusted      []netip.Prefix
}

func New(cfg Config) *Server {
	if cfg.Run == nil {
		cfg.Run = release.Run
	}
	if cfg.Wait <= 0 {
		cfg.Wait = 15 * time.Minute
	}
	if cfg.PreviewTTL <= 0 {
		cfg.PreviewTTL = 72 * time.Hour
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = 5 * time.Minute
	}
	// Un zéro signifie « valeur par défaut » ; une valeur négative désactive
	// explicitement la limite.
	rateLimit := cfg.APIRateLimit
	if rateLimit == 0 {
		rateLimit = DefaultAPIRateLimit
	}
	if rateLimit < 0 {
		rateLimit = 0
	}
	maxConcurrent := cfg.APIMaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = DefaultAPIMaxConcurrent
	}
	if maxConcurrent < 0 {
		maxConcurrent = 0
	}
	return &Server{
		cfg:          cfg,
		planLimiter:  newAPILimiter(30, 5, 2),
		wake:         make(chan struct{}, 1),
		logFollowers: make(chan struct{}, 8),
		limiter:      newAPILimiter(rateLimit, cfg.APIRateBurst, maxConcurrent),
		trusted:      parseTrustedProxies(cfg.TrustedProxies),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routesV2(mux)
	mux.HandleFunc("GET /healthz", s.healthz)
	// The manifest is the only way to declare an application: there is one
	// writer, and it is the compiler. The app endpoints read the state.
	mux.HandleFunc("GET /api/v1/apps", s.withAPI(s.listApps))
	mux.HandleFunc("GET /api/v1/apps/{name}", s.withAPI(s.getApp))
	mux.HandleFunc("GET /api/v1/apps/{name}/status", s.withAPI(s.getAppStatus))
	mux.HandleFunc("GET /api/v1/apps/{name}/logs", s.withAPI(s.getAppLogs))
	mux.HandleFunc("GET /api/v1/apps/{name}/builds", s.withAPI(s.listBuilds))
	mux.HandleFunc("GET /api/v1/apps/{name}/builds/{id}", s.withAPI(s.getBuild))
	mux.HandleFunc("GET /api/v1/apps/{name}/builds/{id}/logs", s.withAPI(s.getBuildLogs))
	mux.HandleFunc("POST /api/v1/apps/{name}/redeploy", s.withAPI(s.redeployApp))
	// Les jetons API sont administrés par l'administrateur seul : créer un
	// jeton de projet ne doit pas être possible avec un jeton de projet.
	mux.HandleFunc("POST /api/v1/tokens", s.withAPI(s.createAPIToken))
	mux.HandleFunc("GET /api/v1/tokens", s.withAPI(s.listAPITokens))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.withAPI(s.revokeAPIToken))
	mux.HandleFunc("POST /api/v1/projects", s.withAPI(s.createProject))
	mux.HandleFunc("GET /api/v1/projects", s.withAPI(s.listProjects))
	mux.HandleFunc("GET /api/v1/projects/{name}", s.withAPI(s.getProject))
	mux.HandleFunc("DELETE /api/v1/projects/{name}", s.withAPI(s.deleteProject))
	mux.HandleFunc("POST /hooks/forgejo", s.webhook)
	// The same endpoint, named after what it is: the forge is not
	// always a Forgejo.
	mux.HandleFunc("POST /hooks/git", s.webhook)
	mux.HandleFunc("POST /hooks/flux", s.fluxHook)
	// Les limites sont posées avant le routeur : une requête refusée ne fait
	// pas de travail. /healthz en est exclu.
	return s.guard(mux)
}

func (s *Server) Start(ctx context.Context) {
	if s.cfg.Store != nil {
		if err := s.cfg.Store.RecoverInterruptedBuilds(ctx); err != nil {
			log.Printf("kuberpack recovery: %v", err)
			return
		}
	}
	// The registry of the platform is resolved first: every build needs it.
	s.resolveRegistry(ctx)
	s.kick()
	go s.worker(ctx)
	// The chart of the control plane must be the one that is applied: the
	// values it writes are rendered with it. It is written before anything
	// is compiled.
	if err := s.ensureChart(ctx); err != nil {
		log.Printf("kuberpack chart: %v", err)
	}
	// The cache of the builder must exist before the first build: a build
	// that finds no volume waits for one that never comes.
	if err := s.ensureBuilderCache(ctx); err != nil {
		log.Printf("kuberpack builder cache: %v", err)
	}
	// The manifest lives in the GitOps repository: the webhook on that
	// repository is what makes it drive the deployment.
	go s.waitForGitOpsHook(ctx)
	// The webhook is the fast path, not the only one: the manifests are
	// read again at startup and then regularly, so a delivery that never
	// arrived does not leave the platform behind.
	go s.reconcileLoop(ctx)
	go s.deploymentLoop(ctx)
}

// reconcileLoop applies the manifests of the GitOps repository, at startup
// and then at every interval. The reconciliation is idempotent: an
// application that did not change is not built again.
func (s *Server) reconcileLoop(ctx context.Context) {
	for {
		s.reconcileSafely(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.cfg.ReconcileInterval):
		}
	}
}

// reconcileSafely applies the manifests. A panic in the reconciliation
// must not take the platform down: the loop goes on, and the platform
// serves the API while it tries again.
func (s *Server) reconcileSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("kuberpack reconcile: panic: %v\n%s", r, debug.Stack())
		}
	}()
	s.reconcileManifests(ctx)
}

// reconcileManifests reads the manifests of every project and applies them.
func (s *Server) reconcileManifests(ctx context.Context) {
	if strings.TrimSpace(s.cfg.GitOpsURL) == "" {
		return
	}
	if err := s.handleGitOpsPush(ctx, pushPayload{}); err != nil {
		log.Printf("kuberpack reconcile: %v", err)
		return
	}
	log.Printf("kuberpack reconcile: the manifests are applied")
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// withAPI authentifie la requête et attache son principal au contexte.
//
// Le jeton global KUBERPACK_API_TOKEN reste l'administrateur de la
// plateforme. Un jeton de la table api_tokens est résolu dans SQLite : son
// secret est comparé à l'empreinte SHA-256 en temps constant, puis son état
// (révoqué, expiré) est vérifié. Un jeton inconnu, révoqué ou expiré reçoit la
// même réponse 401, sans indiquer lequel des contrôles a échoué.
func (s *Server) withAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credential := authorizationCredential(r.Header.Get("Authorization"))
		if credential == "" {
			s.refuseAnonymous(w, r)
			return
		}
		// Comparaison en temps constant, avant toute lecture du corps.
		if s.cfg.APIToken != "" && subtle.ConstantTimeCompare([]byte(credential), []byte(s.cfg.APIToken)) == 1 {
			next(w, r.WithContext(withPrincipal(r.Context(), Principal{
				Role: store.APITokenRoleAdmin,
				Name: "KUBERPACK_API_TOKEN",
			})))
			return
		}
		if s.cfg.Store != nil && strings.HasPrefix(credential, store.APITokenPrefix) {
			token, err := s.cfg.Store.AuthenticateAPIToken(r.Context(), credential, time.Now().UTC())
			if err == nil {
				next(w, r.WithContext(withPrincipal(r.Context(), Principal{
					Role:     token.Role,
					Project:  token.Project,
					TokenID:  token.ID,
					Name:     token.Name,
					Services: token.Services, ExpiresAt: token.ExpiresAt,
				})))
				return
			}
			// Erreur de lecture SQLite : refus explicite, jamais un accès
			// administrateur par défaut.
			apiFailure(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.refuseAnonymous(w, r)
	}
}

// refuseAnonymous distingue l'absence de configuration de l'absence de
// credential : sans jeton global ni jeton de projet, les routes sont fermées.
func (s *Server) refuseAnonymous(w http.ResponseWriter, r *http.Request) {
	if s.cfg.APIToken == "" && s.cfg.Store == nil {
		apiFailure(w, r, http.StatusForbidden, "api token is not configured")
		return
	}
	apiFailure(w, r, http.StatusUnauthorized, "unauthorized")
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	apps, err := s.cfg.Store.ListApps(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	published, err := s.cfg.Store.ListImageApps(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apps = append(apps, published...)
	apps = filterApps(principal, apps)
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	out := s.observedApps(r.Context(), apps)
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		app, err = s.cfg.Store.ImageAppByName(r.Context(), r.PathValue("name"))
	}
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !principal.allowsApp(app) {
		appNotFound(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out := appJSON(app)
	out["deployment"] = s.observeApp(ctx, app)
	writeJSON(w, http.StatusOK, out)
}

type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Before     string `json:"before"`
	Deleted    bool   `json:"deleted"`
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	// Commits carry the files of the push: they tell which projects the
	// push touched.
	Commits []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	repo := strings.ToLower(strings.TrimSpace(payload.Repository.FullName))
	if repo == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The push on the GitOps repository is the manifest itself: it
	// recompiles the projects it touched.
	if s.isGitOpsRepo(repo) {
		s.handleGitOpsWebhook(w, r, payload, body)
		return
	}
	app, err := s.cfg.Store.AppByRepository(r.Context(), repo)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	secret, err := s.cfg.Secrets.Get(app.ID)
	if err != nil || secret == "" {
		http.Error(w, "webhook secret missing", http.StatusInternalServerError)
		return
	}
	if !hmacsig.Valid(secret, hmacsig.Header(r.Header), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := hmacsig.Event(r.Header)
	if event == "" {
		event = "push"
	}
	switch event {
	case "push":
		s.queuePush(w, r, app, payload, body)
	case "pull_request":
		s.queuePullRequest(w, r, app, body)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Server) worker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.drain(ctx)
		s.alignTracks(ctx)
		s.gcPreviews(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// drain processes the queued deliveries, one at a time. A delivery is
// attempted at most once per pass: if SQLite refuses to move it out of the
// queue, the same row would otherwise be picked again and again.
func (s *Server) drain(ctx context.Context) {
	attempted := map[string]bool{}
	for {
		if ctx.Err() != nil {
			return
		}
		queued, err := s.cfg.Store.QueuedDeliveries(ctx)
		if err != nil {
			log.Printf("kuberpack queue: %v", err)
			return
		}
		next := -1
		for i, d := range queued {
			if !attempted[d.ID] {
				next = i
				break
			}
		}
		if next < 0 {
			return
		}
		d := queued[next]
		attempted[d.ID] = true
		s.process(ctx, d)
	}
}

func (s *Server) process(ctx context.Context, d store.Delivery) {
	if err := s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "running", ""); err != nil {
		log.Printf("kuberpack delivery %s: %v", d.ID, err)
		return
	}
	if d.EventType == "pull_request" {
		s.processPreview(ctx, d)
		return
	}
	s.processProduction(ctx, d)
}

func (s *Server) processProduction(ctx context.Context, d store.Delivery) {
	app, err := s.cfg.Store.AppByID(ctx, d.AppID)
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	provider, owner, name, err := s.appForge(app)
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	buildID, err := s.cfg.Store.InsertBuild(ctx, store.Build{
		AppID:       app.ID,
		DeliveryID:  d.ID,
		Environment: "production",
		CommitSHA:   d.CommitSHA,
		Status:      "running",
	})
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	if !isThirdParty(app) {
		s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusPending, "Building", git.ProductionContext, appTargetURL(app))
	}

	track := s.trackFor(ctx, app)
	skipPromote := !track.FollowsMain() && !track.MatchesCommit(d.CommitSHA)

	// The cache of the builder is created before the build: a build that
	// finds no volume waits for one that never comes.
	if err := s.ensureBuilderCache(ctx); err != nil {
		log.Printf("kuberpack builder cache: %v", err)
	}

	result, runErr := s.cfg.Run(ctx, release.Request{
		Builder:       s.cfg.Builder,
		BuildID:       buildID,
		DeliveryID:    d.ID,
		Provider:      provider,
		RegistryURL:   s.cfg.RegistryURL,
		RegistryUser:  s.cfg.RegistryUser,
		RegistryToken: s.cfg.RegistryToken,
		ImageName:     app.Name,
		Token:         s.cfg.Token,
		Owner:         owner,
		Name:          name,
		Branch:        app.ProductionBranch,
		SHA:           d.CommitSHA,
		Strategy:      app.Strategy,
		StartCmd:      app.StartCommand,
		RootDirectory: app.RootDirectory,
		BuildCommand:  app.BuildCommand,
		GitOpsURL:     s.cfg.GitOpsURL,
		GitOpsBranch:  s.cfg.GitOpsBranch,
		ChartPath:     s.cfg.ChartPath,
		// The GitOps directory is keyed by the platform app name, not by the
		// Forgejo repository name: registration wrote <apps>/<app>/.
		ValuesPath:          appValuesPath(s.cfg.Platform, app),
		Hostname:            app.Hostname,
		Port:                app.Port,
		Healthcheck:         app.Healthcheck,
		Platform:            s.cfg.Platform,
		Wait:                s.cfg.Wait,
		SkipPromote:         skipPromote,
		BuildsGitURL:        s.cfg.BuildsGitURL,
		BuildsGitAuthScheme: git.AuthScheme(s.cfg.Provider.Name()),
		GitOpsAuthArgs:      s.cfg.Provider.CloneAuth(s.cfg.Token),
		Environment:         "production",
		Egress:              s.cfg.Egress,
		EgressAllow:         s.cfg.EgressAllow,
		Log:                 func(format string, args ...any) { log.Printf(format, args...) },
	})
	finished := store.Build{Status: "succeeded", ArchiveURL: archiveURL(s.cfg.BuildsGitURL, name, d.CommitSHA)}
	// The output of the build is kept with it. A build that fails must
	// say why: the reason is in the output, not in the status of a
	// container.
	finished.Logs = s.buildLogs(ctx, d.ID)
	if runErr != nil {
		finished.Status = "failed"
		finished.Error = runErr.Error()
		_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", runErr.Error())
		target := finished.ArchiveURL
		if target == "" {
			target = commitURL(s.cfg.Provider.WebURL(), owner, name, d.CommitSHA)
		}
		if !isThirdParty(app) {
			s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusFailure, runErr.Error(), git.ProductionContext, target)
		}
		log.Printf("kuberpack build %s %s: %v", app.Name, d.CommitSHA, runErr)
		return
	}
	finished.ImageRepository = result.Image.Repository
	finished.ImageTag = result.Image.Tag
	finished.ImageDigest = result.Image.Digest
	finished.InfraCommitSHA = result.InfraCommitSHA
	_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
	_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "succeeded", "")
	target := appTargetURL(app)
	if finished.ArchiveURL != "" {
		target = finished.ArchiveURL
	} else if skipPromote {
		target = appTargetURL(app)
	}
	if skipPromote {
		if !isThirdParty(app) {
			s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusWarning, "Built, frozen at "+track.Short(), git.ProductionContext, appTargetURL(app))
		}
	} else {
		if !isThirdParty(app) {
			s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusSuccess, "Deployed", git.ProductionContext, target)
		}
	}
	s.alignTrack(ctx, app)
}

func (s *Server) publishCommitStatus(ctx context.Context, provider git.Provider, owner, repo, sha, state, description string, app store.App, failed bool) {
	target := appTargetURL(app)
	if failed && provider != nil {
		target = commitURL(provider.WebURL(), owner, repo, sha)
	}
	s.publishStatus(ctx, provider, owner, repo, sha, state, description, git.ProductionContext, target)
}

func (s *Server) publishStatus(ctx context.Context, provider git.Provider, owner, repo, sha, state, description, contextName, target string) {
	if provider == nil {
		return
	}
	if contextName == "" {
		contextName = git.ProductionContext
	}
	err := provider.CommitStatus(ctx, owner, repo, sha, git.Status{
		State:       state,
		Context:     contextName,
		Description: fail.Short(description),
		TargetURL:   target,
	})
	if err != nil {
		log.Printf("kuberpack commit status %s/%s %s: %v", owner, repo, sha, err)
	}
}

func commitURL(base, owner, repo, sha string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || owner == "" || repo == "" || sha == "" {
		return ""
	}
	return base + "/" + owner + "/" + repo + "/commit/" + sha
}

func appTargetURL(app store.App) string {
	host := strings.TrimSpace(app.Hostname)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		return host
	}
	return "https://" + host
}

func appJSON(app store.App) map[string]any {
	return map[string]any{
		"id":                  app.ID,
		"name":                app.Name,
		"project":             app.Project,
		"namespace":           app.Namespace,
		"values_path":         app.ValuesPath,
		"repository":          app.ForgejoRepository,
		"image":               app.Image,
		"branch":              app.ProductionBranch,
		"strategy":            app.Strategy,
		"autodeploy":          app.Autodeploy,
		"autodeploy_pr":       app.AutodeployPR,
		"hostname":            app.Hostname,
		"port":                app.Port,
		"healthcheck":         app.Healthcheck,
		"start_command":       app.StartCommand,
		"root_directory":      app.RootDirectory,
		"build_command":       app.BuildCommand,
		"internet":            app.Internet,
		"peers":               app.Peers,
		"secrets":             app.Secrets,
		"secret_env":          app.SecretEnv,
		"inherit_secret_from": app.InheritSecretFrom,
		"preview_database":    app.PreviewDatabase,
		"webhook_id":          app.WebhookID,
	}
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

func readJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return fmt.Errorf("payload too large")
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("invalid json")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

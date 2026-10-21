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
	"strconv"
	"strings"
	"time"
	"unicode"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

// deliveryNonce returns a short random suffix for a delivery id. An explicit
// user action (register, redeploy) must always queue a new build, even for a
// commit that was already built: the delivery id is the primary key of
// webhook_deliveries, so a repeated one would be dropped as a duplicate.
func deliveryNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
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
	RegistryUser string
	Token        string
	APIToken     string
	WebhookURL   string
	GitOpsURL    string
	GitOpsBranch string
	ChartPath    string
	Platform     promote.Platform
	Wait         time.Duration
	PreviewTTL   time.Duration
	BuildsGitURL string
	FluxSecret   string
	Run          func(context.Context, release.Request) (release.Result, error)
	GitOpsFile   func(context.Context, store.App) ([]byte, error)
}

type Server struct {
	cfg  Config
	wake chan struct{}
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
	return &Server{cfg: cfg, wake: make(chan struct{}, 1)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	// The manifest is the only way to declare an application: there is one
	// writer, and it is the compiler. The app endpoints read the state.
	mux.HandleFunc("GET /api/v1/apps", s.withAPI(s.listApps))
	mux.HandleFunc("GET /api/v1/apps/{name}", s.withAPI(s.getApp))
	mux.HandleFunc("GET /api/v1/apps/{name}/builds", s.withAPI(s.listBuilds))
	mux.HandleFunc("POST /api/v1/apps/{name}/redeploy", s.withAPI(s.redeployApp))
	mux.HandleFunc("POST /api/v1/projects", s.withAPI(s.createProject))
	mux.HandleFunc("GET /api/v1/projects", s.withAPI(s.listProjects))
	mux.HandleFunc("GET /api/v1/projects/{name}", s.withAPI(s.getProject))
	mux.HandleFunc("DELETE /api/v1/projects/{name}", s.withAPI(s.deleteProject))
	mux.HandleFunc("POST /hooks/forgejo", s.webhook)
	// The same endpoint, named after what it is: the forge is not
	// always a Forgejo.
	mux.HandleFunc("POST /hooks/git", s.webhook)
	mux.HandleFunc("POST /hooks/flux", s.fluxHook)
	return mux
}

func (s *Server) Start(ctx context.Context) {
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
	// The manifest lives in the GitOps repository: the webhook on that
	// repository is what makes it drive the deployment.
	go s.waitForGitOpsHook(ctx)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) withAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			http.Error(w, "api token is not configured", http.StatusForbidden)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if got == "" {
			got = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "token "))
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.cfg.Store.ListApps(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		out = append(out, appJSON(app))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, appJSON(app))
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
	s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusPending, "Building", git.ProductionContext, appTargetURL(app))

	track := s.trackFor(ctx, app)
	skipPromote := !track.FollowsMain() && !track.MatchesCommit(d.CommitSHA)

	result, runErr := s.cfg.Run(ctx, release.Request{
		Builder:       s.cfg.Builder,
		BuildID:       buildID,
		DeliveryID:    d.ID,
		Provider:      provider,
		RegistryURL:   s.cfg.RegistryURL,
		RegistryUser:  s.cfg.RegistryUser,
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
		Log:                 func(format string, args ...any) { log.Printf(format, args...) },
	})
	finished := store.Build{Status: "succeeded", ArchiveURL: archiveURL(s.cfg.BuildsGitURL, name, d.CommitSHA)}
	if runErr != nil {
		finished.Status = "failed"
		finished.Error = runErr.Error()
		_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", runErr.Error())
		target := finished.ArchiveURL
		if target == "" {
			target = commitURL(s.cfg.Provider.WebURL(), owner, name, d.CommitSHA)
		}
		s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusFailure, runErr.Error(), git.ProductionContext, target)
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
		s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusWarning, "Built, frozen at "+track.Short(), git.ProductionContext, appTargetURL(app))
	} else {
		s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusSuccess, "Deployed", git.ProductionContext, target)
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
		Description: description,
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
		"values_path":         app.ValuesPath,
		"repository":          app.ForgejoRepository,
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

func secretEnvOrDefault(env string) string {
	if env = strings.TrimSpace(env); env != "" {
		return env
	}
	return "prod"
}

// inheritSecretFromOrDefault normalizes to "prod" or "dev"; anything else
// (including empty) defaults to "dev".
func inheritSecretFromOrDefault(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), promote.SecretFromProd) {
		return promote.SecretFromProd
	}
	return promote.SecretFromDev
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

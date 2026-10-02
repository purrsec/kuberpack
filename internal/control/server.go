package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

const maxBody = 1 << 20

type Config struct {
	Builder      builder.Runner
	Store        *store.Store
	Secrets      store.SecretsDir
	Client       *forgejo.Client
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
	mux.HandleFunc("POST /api/v1/apps", s.withAPI(s.createApp))
	mux.HandleFunc("GET /api/v1/apps", s.withAPI(s.listApps))
	mux.HandleFunc("GET /api/v1/apps/{name}", s.withAPI(s.getApp))
	mux.HandleFunc("PATCH /api/v1/apps/{name}", s.withAPI(s.patchApp))
	mux.HandleFunc("DELETE /api/v1/apps/{name}", s.withAPI(s.deleteApp))
	mux.HandleFunc("GET /api/v1/apps/{name}/builds", s.withAPI(s.listBuilds))
	mux.HandleFunc("POST /api/v1/apps/{name}/redeploy", s.withAPI(s.redeployApp))
	mux.HandleFunc("POST /hooks/forgejo", s.webhook)
	mux.HandleFunc("POST /hooks/flux", s.fluxHook)
	return mux
}

func (s *Server) Start(ctx context.Context) {
	s.kick()
	go s.worker(ctx)
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

type createAppBody struct {
	Name         string   `json:"name"`
	Repository   string   `json:"repository"`
	Branch       string   `json:"branch"`
	Strategy     string   `json:"strategy"`
	Autodeploy   *bool    `json:"autodeploy"`
	AutodeployPR bool     `json:"autodeploy_pr"`
	Hostname     string   `json:"hostname"`
	Port         int      `json:"port"`
	Healthcheck  string   `json:"healthcheck"`
	StartCommand string   `json:"start_command"`
	Internet     *bool    `json:"internet"`
	Peers        []string `json:"peers"`
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var body createAppBody
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := sanitizeName(body.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	owner, repoName, err := forgejo.ParseOwnerName(body.Repository)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	branch := strings.TrimSpace(body.Branch)
	if branch == "" {
		branch = "main"
	}
	strategyName := strings.TrimSpace(body.Strategy)
	if strategyName == "" {
		strategyName = "auto"
	}
	if _, err := strategy.ParseKind(strategyName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Hostname) == "" {
		http.Error(w, "hostname is required", http.StatusBadRequest)
		return
	}
	port := body.Port
	if port == 0 {
		port = 8080
	}
	if port < 1 || port > 65535 {
		http.Error(w, "port is invalid", http.StatusBadRequest)
		return
	}
	health := strings.TrimSpace(body.Healthcheck)
	if health == "" {
		health = "/"
	}
	auto := true
	if body.Autodeploy != nil {
		auto = *body.Autodeploy
	}
	internet := true
	if body.Internet != nil {
		internet = *body.Internet
	}
	peers, err := promote.ParsePeers(body.Peers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := s.cfg.Client.Repo(r.Context(), owner, repoName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app, err := s.cfg.Store.InsertApp(r.Context(), store.App{
		Name:              name,
		ForgejoRepository: owner + "/" + repoName,
		ProductionBranch:  branch,
		Strategy:          strategyName,
		Autodeploy:        auto,
		AutodeployPR:      body.AutodeployPR,
		Hostname:          strings.TrimSpace(body.Hostname),
		Port:              port,
		Healthcheck:       health,
		StartCommand:      strings.TrimSpace(body.StartCommand),
		Internet:          internet,
		Peers:             promote.FormatPeers(peers),
	})
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Registration performs several external side effects. From here on, any
	// failure rolls the whole registration back so a retry starts clean
	// instead of leaving an orphan app / HMAC secret / webhook behind.
	rollback := func() {
		if s.cfg.WebhookURL != "" && app.WebhookID > 0 {
			_ = s.cfg.Client.DeleteHook(r.Context(), owner, repoName, app.WebhookID)
		}
		_ = s.cfg.Secrets.Delete(app.ID)
		_ = s.cfg.Store.DeleteApp(r.Context(), app.ID)
	}

	secret, err := hmacsig.NewSecret()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.cfg.Secrets.Put(app.ID, secret); err != nil {
		_ = s.cfg.Store.DeleteApp(r.Context(), app.ID)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := appJSON(app)
	if s.cfg.WebhookURL != "" {
		hook, err := s.cfg.Client.EnsureHook(r.Context(), owner, repoName, s.cfg.WebhookURL, secret)
		if err != nil {
			rollback()
			http.Error(w, "webhook failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.cfg.Store.SetWebhookID(r.Context(), app.ID, hook.ID); err != nil {
			_ = s.cfg.Client.DeleteHook(r.Context(), owner, repoName, hook.ID)
			rollback()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		app.WebhookID = hook.ID
		resp = appJSON(app)
		resp["webhook_configured"] = true
	} else {
		resp["webhook_configured"] = false
		resp["webhook_secret"] = secret
	}

	if s.cfg.GitOpsURL != "" {
		if _, err := promote.EnsureApp(r.Context(), promote.EnsureRequest{
			GitOpsURL:    s.cfg.GitOpsURL,
			GitOpsBranch: s.cfg.GitOpsBranch,
			HTTPToken:    s.cfg.Token,
			App: promote.AppSpec{
				Name:        app.Name,
				Hostname:    app.Hostname,
				Port:        app.Port,
				Healthcheck: app.Healthcheck,
				Internet:    &app.Internet,
				Peers:       app.Peers,
			},
			Platform: s.cfg.Platform,
		}); err != nil {
			// The GitOps files were not written (EnsureApp is all-or-nothing
			// before its push), so undoing the external state is enough.
			rollback()
			http.Error(w, "gitops failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		resp["gitops_configured"] = true
	}

	if auto {
		sha, err := s.cfg.Client.BranchSHA(r.Context(), owner, repoName, branch)
		if err == nil && sha != "" {
			del := "register-" + sha
			if err := s.cfg.Store.InsertDelivery(r.Context(), store.Delivery{
				ID:        del,
				AppID:     app.ID,
				EventType: "register",
				CommitSHA: sha,
				Status:    "queued",
			}); err == nil || errors.Is(err, store.ErrDuplicate) {
				s.publishCommitStatus(r.Context(), owner, repoName, sha, forgejo.StatusPending, "Building", app, false)
				s.kick()
			}
		}
	}

	writeJSON(w, http.StatusCreated, resp)
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
	Deleted    bool   `json:"deleted"`
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
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

func (s *Server) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		queued, err := s.cfg.Store.QueuedDeliveries(ctx)
		if err != nil {
			log.Printf("kuberpack queue: %v", err)
			return
		}
		if len(queued) == 0 {
			return
		}
		d := queued[0]
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
	owner, name, err := forgejo.ParseOwnerName(app.ForgejoRepository)
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
	s.publishStatus(ctx, owner, name, d.CommitSHA, forgejo.StatusPending, "Building", forgejo.ProductionContext, appTargetURL(app))

	track := s.trackFor(ctx, app)
	skipPromote := !track.FollowsMain() && !track.MatchesCommit(d.CommitSHA)

	result, runErr := s.cfg.Run(ctx, release.Request{
		Builder:      s.cfg.Builder,
		BuildID:      buildID,
		DeliveryID:   d.ID,
		Client:       s.cfg.Client,
		Token:        s.cfg.Token,
		Owner:        owner,
		Name:         name,
		Branch:       app.ProductionBranch,
		SHA:          d.CommitSHA,
		Strategy:     app.Strategy,
		StartCmd:     app.StartCommand,
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		ChartPath:    s.cfg.ChartPath,
		Hostname:     app.Hostname,
		Port:         app.Port,
		Healthcheck:  app.Healthcheck,
		Platform:     s.cfg.Platform,
		Wait:         s.cfg.Wait,
		SkipPromote:  skipPromote,
		BuildsGitURL: s.cfg.BuildsGitURL,
		Environment:  "production",
		Log:          func(format string, args ...any) { log.Printf(format, args...) },
	})
	finished := store.Build{Status: "succeeded", ArchiveURL: archiveURL(s.cfg.BuildsGitURL, name, d.CommitSHA)}
	if runErr != nil {
		finished.Status = "failed"
		finished.Error = runErr.Error()
		_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", runErr.Error())
		target := finished.ArchiveURL
		if target == "" {
			target = commitURL(s.cfg.Client.BaseURL, owner, name, d.CommitSHA)
		}
		s.publishStatus(ctx, owner, name, d.CommitSHA, forgejo.StatusFailure, runErr.Error(), forgejo.ProductionContext, target)
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
		s.publishStatus(ctx, owner, name, d.CommitSHA, forgejo.StatusWarning, "Built, frozen at "+track.Short(), forgejo.ProductionContext, appTargetURL(app))
	} else {
		s.publishStatus(ctx, owner, name, d.CommitSHA, forgejo.StatusSuccess, "Deployed", forgejo.ProductionContext, target)
	}
	s.alignTrack(ctx, app)
}

func (s *Server) publishCommitStatus(ctx context.Context, owner, repo, sha, state, description string, app store.App, failed bool) {
	target := appTargetURL(app)
	if failed {
		target = commitURL(s.cfg.Client.BaseURL, owner, repo, sha)
	}
	s.publishStatus(ctx, owner, repo, sha, state, description, forgejo.ProductionContext, target)
}

func (s *Server) publishStatus(ctx context.Context, owner, repo, sha, state, description, contextName, target string) {
	if s.cfg.Client == nil {
		return
	}
	if contextName == "" {
		contextName = forgejo.ProductionContext
	}
	err := s.cfg.Client.CreateCommitStatus(ctx, owner, repo, sha, forgejo.CommitStatus{
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
		"id":            app.ID,
		"name":          app.Name,
		"repository":    app.ForgejoRepository,
		"branch":        app.ProductionBranch,
		"strategy":      app.Strategy,
		"autodeploy":    app.Autodeploy,
		"autodeploy_pr": app.AutodeployPR,
		"hostname":      app.Hostname,
		"port":          app.Port,
		"healthcheck":   app.Healthcheck,
		"internet":      app.Internet,
		"peers":         app.Peers,
		"webhook_id":    app.WebhookID,
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

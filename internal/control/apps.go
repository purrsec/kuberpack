package control

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

type patchAppBody struct {
	Branch            *string   `json:"branch"`
	Strategy          *string   `json:"strategy"`
	Autodeploy        *bool     `json:"autodeploy"`
	AutodeployPR      *bool     `json:"autodeploy_pr"`
	Hostname          *string   `json:"hostname"`
	Port              *int      `json:"port"`
	Healthcheck       *string   `json:"healthcheck"`
	StartCommand      *string   `json:"start_command"`
	RootDirectory     *string   `json:"root_directory"`
	BuildCommand      *string   `json:"build_command"`
	Internet          *bool     `json:"internet"`
	Peers             *[]string `json:"peers"`
	Secrets           *[]string `json:"secrets"`
	SecretEnv         *string   `json:"secret_env"`
	InheritSecretFrom *string   `json:"inherit_secret_from"`
}

func (s *Server) patchApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body patchAppBody
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Branch != nil {
		app.ProductionBranch = strings.TrimSpace(*body.Branch)
	}
	if body.Strategy != nil {
		if _, err := strategy.ParseKind(strings.TrimSpace(*body.Strategy)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		app.Strategy = strings.TrimSpace(*body.Strategy)
	}
	if body.Autodeploy != nil {
		app.Autodeploy = *body.Autodeploy
	}
	if body.AutodeployPR != nil {
		app.AutodeployPR = *body.AutodeployPR
	}
	if body.Hostname != nil {
		app.Hostname = strings.TrimSpace(*body.Hostname)
	}
	if body.Port != nil {
		if *body.Port < 1 || *body.Port > 65535 {
			http.Error(w, "port is invalid", http.StatusBadRequest)
			return
		}
		app.Port = *body.Port
	}
	if body.Healthcheck != nil {
		app.Healthcheck = strings.TrimSpace(*body.Healthcheck)
	}
	if body.StartCommand != nil {
		app.StartCommand = strings.TrimSpace(*body.StartCommand)
	}
	if body.RootDirectory != nil {
		app.RootDirectory = strings.TrimSpace(*body.RootDirectory)
	}
	if body.BuildCommand != nil {
		app.BuildCommand = strings.TrimSpace(*body.BuildCommand)
	}
	if body.Internet != nil {
		app.Internet = *body.Internet
	}
	if body.Peers != nil {
		peers, err := promote.ParsePeers(*body.Peers)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		app.Peers = promote.FormatPeers(peers)
	}
	if body.Secrets != nil {
		app.Secrets = promote.NormalizeSecretKeys(*body.Secrets)
	}
	if body.SecretEnv != nil {
		app.SecretEnv = secretEnvOrDefault(*body.SecretEnv)
	}
	if body.InheritSecretFrom != nil {
		app.InheritSecretFrom = inheritSecretFromOrDefault(*body.InheritSecretFrom)
	}
	previous := app
	if err := s.cfg.Store.UpdateApp(r.Context(), app); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Git is the source of truth for the runtime contract: propagate every
	// change (healthcheck, port, hostname, networkPolicy), not just the network.
	if s.cfg.GitOpsURL != "" {
		if _, err := promote.SyncRuntime(r.Context(), promote.EnsureRequest{
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
				Secrets:     app.Secrets,
				SecretEnv:   app.SecretEnv,
			},
			Platform: s.cfg.Platform,
		}); err != nil {
			// Keep SQLite in phase with GitOps: roll the update back so a retry
			// starts from the previous state instead of leaving a silent drift.
			if rollbackErr := s.cfg.Store.UpdateApp(r.Context(), previous); rollbackErr != nil {
				log.Printf("kuberpack patch %s rollback: %v", app.Name, rollbackErr)
			}
			http.Error(w, "gitops runtime update failed, update rolled back: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	updated, err := s.cfg.Store.AppByID(r.Context(), app.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, appJSON(updated))
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner, name, err := forgejo.ParseOwnerName(app.ForgejoRepository)
	if err == nil && app.WebhookID > 0 {
		_ = s.cfg.Client.DeleteHook(r.Context(), owner, name, app.WebhookID)
	}
	if s.cfg.GitOpsURL != "" {
		_, _ = promote.DisableApp(r.Context(), promote.EnsureRequest{
			GitOpsURL:    s.cfg.GitOpsURL,
			GitOpsBranch: s.cfg.GitOpsBranch,
			HTTPToken:    s.cfg.Token,
			App:          promote.AppSpec{Name: app.Name, Hostname: app.Hostname, Port: app.Port, Healthcheck: app.Healthcheck},
			Platform:     s.cfg.Platform,
		})
	}
	_ = s.cfg.Secrets.Delete(app.ID)
	if err := s.cfg.Store.DeleteApp(r.Context(), app.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listBuilds(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	builds, err := s.cfg.Store.ListBuilds(r.Context(), app.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(builds))
	for _, b := range builds {
		out = append(out, map[string]any{
			"id":           b.ID,
			"environment":  b.Environment,
			"commit_sha":   b.CommitSHA,
			"image":        strings.TrimSpace(strings.TrimSuffix(b.ImageRepository+":"+b.ImageTag, ":")),
			"digest":       b.ImageDigest,
			"infra_commit": b.InfraCommitSHA,
			"status":       b.Status,
			"error":        b.Error,
			"archive_url":  b.ArchiveURL,
			"created_at":   b.CreatedAt,
			"finished_at":  b.FinishedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"builds": out})
}

func (s *Server) redeployApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner, name, err := forgejo.ParseOwnerName(app.ForgejoRepository)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sha, err := s.cfg.Client.BranchSHA(r.Context(), owner, name, app.ProductionBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	del := "redeploy-" + sha
	if err := s.cfg.Store.InsertDelivery(r.Context(), store.Delivery{
		ID: del, AppID: app.ID, EventType: "redeploy", CommitSHA: sha, Status: "queued",
	}); err != nil && !errors.Is(err, store.ErrDuplicate) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishCommitStatus(r.Context(), owner, name, sha, forgejo.StatusPending, "Building", app, false)
	s.kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "commit_sha": sha})
}

func (s *Server) fluxHook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.FluxSecret == "" {
		http.Error(w, "flux webhook is not configured", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if !hmacsig.Valid(s.cfg.FluxSecret, hmacsig.Header(r.Header), body) && strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+s.cfg.FluxSecret {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var payload struct {
		Severity       string `json:"severity"`
		Message        string `json:"message"`
		InvolvedObject struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"involvedObject"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(payload.InvolvedObject.Kind, "HelmRelease") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sev := strings.ToLower(payload.Severity)
	if sev != "error" && sev != "failure" && !strings.Contains(strings.ToLower(payload.Message), "rollback") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	app, err := s.cfg.Store.AppByName(r.Context(), payload.InvolvedObject.Name)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := s.cfg.Store.LatestProductionBuild(r.Context(), app.ID)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg := strings.TrimSpace(payload.Message)
	if msg == "" {
		msg = "HelmRelease rolled back"
	}
	if err := s.cfg.Store.MarkRolledBack(r.Context(), b.ID, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner, name, parseErr := forgejo.ParseOwnerName(app.ForgejoRepository)
	if parseErr == nil {
		s.publishStatus(r.Context(), owner, name, b.CommitSHA, forgejo.StatusWarning, "rolled_back: "+msg, forgejo.ProductionContext, appTargetURL(app))
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rolled_back"})
}

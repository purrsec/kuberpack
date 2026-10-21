package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

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
	owner, name, err := git.SplitOwnerName(app.ForgejoRepository)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sha, err := s.cfg.Provider.BranchSHA(r.Context(), owner, name, app.ProductionBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// delivery_id is globally unique: scope it to the app so two apps built
	// from the same commit do not collide (the second redeploy would be
	// silently dropped as a duplicate). The nonce keeps it unique per
	// request, so a redeploy always queues a new build instead of being
	// dropped as a duplicate of an earlier one.
	del := "redeploy-" + app.Name + "-" + sha + "-" + deliveryNonce()
	if err := s.cfg.Store.InsertDelivery(r.Context(), store.Delivery{
		ID: del, AppID: app.ID, EventType: "redeploy", CommitSHA: sha, Status: "queued",
	}); err != nil && !errors.Is(err, store.ErrDuplicate) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishCommitStatus(r.Context(), owner, name, sha, git.StatusPending, "Building", app, false)
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
	owner, name, parseErr := git.SplitOwnerName(app.ForgejoRepository)
	if parseErr == nil {
		s.publishStatus(r.Context(), owner, name, b.CommitSHA, git.StatusWarning, "rolled_back: "+msg, git.ProductionContext, appTargetURL(app))
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rolled_back"})
}

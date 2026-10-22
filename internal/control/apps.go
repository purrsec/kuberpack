package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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
		out = append(out, buildJSON(b))
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
	provider, owner, name, err := s.appForge(app)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sha, err := provider.BranchSHA(r.Context(), owner, name, app.ProductionBranch)
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
	s.publishCommitStatus(r.Context(), provider, owner, name, sha, git.StatusPending, "Building", app, false)
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
	provider, owner, name, parseErr := s.appForge(app)
	if parseErr == nil {
		s.publishStatus(r.Context(), provider, owner, name, b.CommitSHA, git.StatusWarning, "rolled_back: "+msg, git.ProductionContext, appTargetURL(app))
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rolled_back"})
}

// getBuild returns one build of an application.
//
// The reason of a failure is in the build: it is what the platform knows
// about it, and the logs are next to it.
func (s *Server) getBuild(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "the build id must be a number", http.StatusBadRequest)
		return
	}
	build, err := s.cfg.Store.BuildByID(r.Context(), app.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, buildJSON(build))
}

// getBuildLogs returns the output of the builder of a build.
//
// The output of a build that is over is kept with it. The output of a
// build that runs is read from the pod, so that the client can follow
// it.
func (s *Server) getBuildLogs(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "the build id must be a number", http.StatusBadRequest)
		return
	}
	build, err := s.cfg.Store.BuildByID(r.Context(), app.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logs := build.Logs
	if strings.TrimSpace(logs) == "" {
		// The build may still be running: its output is then read from
		// the pod of the build.
		logs = s.buildLogs(r.Context(), build.DeliveryID)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(logs))
}

// buildJSON is the representation of a build.
func buildJSON(b store.Build) map[string]any {
	return map[string]any{
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
		"has_logs":     strings.TrimSpace(b.Logs) != "",
	}
}

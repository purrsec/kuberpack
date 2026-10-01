package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func (s *Server) queuePush(w http.ResponseWriter, r *http.Request, app store.App, payload pushPayload, _ []byte) {
	if !app.Autodeploy || payload.Deleted || payload.After == "" || strings.Trim(payload.After, "0") == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	wantRef := "refs/heads/" + app.ProductionBranch
	if payload.Ref != wantRef {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delivery := hmacsig.DeliveryID(r.Header)
	if delivery == "" {
		delivery = "push-" + payload.After
	}
	err := s.cfg.Store.InsertDelivery(r.Context(), store.Delivery{
		ID:        delivery,
		AppID:     app.ID,
		EventType: "push",
		Action:    payload.Action,
		CommitSHA: strings.ToLower(payload.After),
		Status:    "queued",
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate"})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner, name, parseErr := forgejo.ParseOwnerName(app.ForgejoRepository)
	if parseErr == nil {
		s.publishCommitStatus(r.Context(), owner, name, payload.After, forgejo.StatusPending, "Building", app, false)
	}
	s.kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

type prPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int    `json:"number"`
		Head   prHead `json:"head"`
		Base   prHead `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type prHead struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo struct {
		FullName string `json:"full_name"`
		Fork     bool   `json:"fork"`
	} `json:"repo"`
}

func (s *Server) queuePullRequest(w http.ResponseWriter, r *http.Request, app store.App, body []byte) {
	if !app.AutodeployPR {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var payload prPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	n := payload.PullRequest.Number
	if n == 0 {
		n = payload.Number
	}
	if n <= 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	headRepo := strings.ToLower(strings.TrimSpace(payload.PullRequest.Head.Repo.FullName))
	baseRepo := strings.ToLower(strings.TrimSpace(app.ForgejoRepository))
	if payload.PullRequest.Head.Repo.Fork || (headRepo != "" && headRepo != baseRepo) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sha := strings.ToLower(strings.TrimSpace(payload.PullRequest.Head.SHA))
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	delivery := hmacsig.DeliveryID(r.Header)
	if delivery == "" {
		delivery = "pr-" + action + "-" + app.Name + "-" + strconv.Itoa(n) + "-" + sha
	}
	switch action {
	case "opened", "reopened", "synchronize", "closed":
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	err := s.cfg.Store.InsertDelivery(r.Context(), store.Delivery{
		ID:          delivery,
		AppID:       app.ID,
		EventType:   "pull_request",
		Action:      action,
		CommitSHA:   sha,
		PullRequest: n,
		Status:      "queued",
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate"})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if action != "closed" && sha != "" {
		owner, name, parseErr := forgejo.ParseOwnerName(app.ForgejoRepository)
		if parseErr == nil {
			host := promotePreviewHost(app.Name, n, s.cfg.Platform.PreviewDomain)
			s.publishStatus(r.Context(), owner, name, sha, forgejo.StatusPending, "Building preview", forgejo.PreviewContext, "https://"+host)
		}
	}
	s.kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

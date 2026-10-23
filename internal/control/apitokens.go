package control

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/store"
)

type apiTokenBody struct {
	Name string `json:"name"`
	// Project vide crée un jeton administrateur ; sinon le jeton est limité
	// à ce projet.
	Project string `json:"project"`
	// ExpiresIn est une durée Go ("720h", "30m"). Vide : aucun expiration.
	ExpiresIn string `json:"expires_in"`
}

// createAPIToken crée un jeton et le renvoie une seule fois en clair.
//
// Le secret n'est ni journalisé, ni relisible ensuite : seule son empreinte
// SHA-256 entre dans SQLite. La réponse est le seul endroit où il apparaît.
func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.cfg.Store == nil {
		http.Error(w, "the token store is unavailable", http.StatusInternalServerError)
		return
	}
	var body apiTokenBody
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(name) > 120 {
		http.Error(w, "name is too long", http.StatusBadRequest)
		return
	}
	project := strings.TrimSpace(body.Project)
	if project != "" {
		// Le nom du projet est celui du manifeste, déjà normalisé. Refuser
		// une autre forme évite un jeton qui ne correspondrait à rien.
		if sanitizeName(project) != project {
			http.Error(w, "project must be a normalized name (lowercase letters, digits and dashes)", http.StatusBadRequest)
			return
		}
	}
	role := store.APITokenRoleAdmin
	if project != "" {
		role = store.APITokenRoleProject
	}
	var expiresAt time.Time
	if raw := strings.TrimSpace(body.ExpiresIn); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			http.Error(w, "expires_in must be a positive duration, for example 720h", http.StatusBadRequest)
			return
		}
		expiresAt = time.Now().UTC().Add(duration)
	}
	token, secret, err := s.cfg.Store.CreateAPIToken(r.Context(), name, project, role, expiresAt)
	if err != nil {
		http.Error(w, "cannot create the token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         token.ID,
		"name":       token.Name,
		"project":    token.Project,
		"role":       token.Role,
		"created_at": formatAPITokenTime(token.CreatedAt),
		"expires_at": formatAPITokenTime(token.ExpiresAt),
		"token":      secret,
	})
}

// listAPITokens liste les jetons sans jamais renvoyer d'empreinte ni de
// secret. Les jetons révoqués restent visibles avec leur date.
func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.cfg.Store == nil {
		http.Error(w, "the token store is unavailable", http.StatusInternalServerError)
		return
	}
	tokens, err := s.cfg.Store.ListAPITokens(r.Context())
	if err != nil {
		http.Error(w, "cannot read tokens", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, map[string]any{
			"id":           token.ID,
			"name":         token.Name,
			"project":      token.Project,
			"role":         token.Role,
			"status":       apiTokenStatus(token, now),
			"created_at":   formatAPITokenTime(token.CreatedAt),
			"last_used_at": formatAPITokenTime(token.LastUsedAt),
			"expires_at":   formatAPITokenTime(token.ExpiresAt),
			"revoked_at":   formatAPITokenTime(token.RevokedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// revokeAPIToken invalide un jeton. La révocation est idempotente.
func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.cfg.Store == nil {
		http.Error(w, "the token store is unavailable", http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "the token id must be a positive number", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.RevokeAPIToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot revoke the token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
}

func apiTokenStatus(token store.APIToken, now time.Time) string {
	if !token.RevokedAt.IsZero() {
		return "revoked"
	}
	if !token.ExpiresAt.IsZero() && !now.Before(token.ExpiresAt) {
		return "expired"
	}
	return "active"
}

func formatAPITokenTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

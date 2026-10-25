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
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Services []string `json:"services,omitempty"`
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
		apiFailure(w, r, http.StatusInternalServerError, "the token store is unavailable")
		return
	}
	var body apiTokenBody
	if err := readJSON(r, &body); err != nil {
		apiFailure(w, r, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		apiFailure(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if len(name) > 120 {
		apiFailure(w, r, http.StatusBadRequest, "name is too long")
		return
	}
	project := strings.TrimSpace(body.Project)
	if project != "" {
		// Le nom du projet est celui du manifeste, déjà normalisé. Refuser
		// une autre forme évite un jeton qui ne correspondrait à rien.
		if sanitizeName(project) != project {
			apiFailure(w, r, http.StatusBadRequest, "project must be a normalized name (lowercase letters, digits and dashes)")
			return
		}
	}
	role := body.Role
	if role != store.APITokenRoleAdmin && role != store.APITokenRoleReader && role != store.APITokenRoleDeployer && role != store.APITokenRoleMaintainer {
		apiError(w, 400, "invalid_role", "an explicit role admin, reader, deployer or maintainer is required")
		return
	}
	if (role == store.APITokenRoleAdmin) != (project == "") {
		apiError(w, 400, "invalid_scope", "project is required for project roles and forbidden for admin")
		return
	}
	if len(body.Services) > 0 && role != store.APITokenRoleDeployer {
		apiError(w, 400, "invalid_scope", "service restrictions require deployer role")
		return
	}
	for _, name := range body.Services {
		if sanitizeName(name) != name || name == "" {
			apiError(w, 400, "invalid_scope", "invalid service name")
			return
		}
	}
	if strings.TrimSpace(body.ExpiresIn) == "" {
		apiError(w, 400, "expiration_required", "expires_in is required")
		return
	}
	var expiresAt time.Time
	if raw := strings.TrimSpace(body.ExpiresIn); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			apiFailure(w, r, http.StatusBadRequest, "expires_in must be a positive duration, for example 720h")
			return
		}
		expiresAt = time.Now().UTC().Add(duration)
	}
	token, secret, err := s.cfg.Store.CreateAPIToken(r.Context(), name, project, role, expiresAt)
	if err != nil {
		apiFailure(w, r, http.StatusInternalServerError, "cannot create the token")
		return
	}
	if err := s.cfg.Store.SetAPITokenServices(r.Context(), token.ID, body.Services); err != nil {
		_ = s.cfg.Store.RevokeAPIToken(r.Context(), token.ID)
		apiError(w, 500, "storage_error", "cannot restrict the token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         token.ID,
		"name":       token.Name,
		"project":    token.Project,
		"role":       token.Role,
		"services":   body.Services,
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
		apiFailure(w, r, http.StatusInternalServerError, "the token store is unavailable")
		return
	}
	tokens, err := s.cfg.Store.ListAPITokens(r.Context())
	if err != nil {
		apiFailure(w, r, http.StatusInternalServerError, "cannot read tokens")
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
			"services":     token.Services,
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
		apiFailure(w, r, http.StatusInternalServerError, "the token store is unavailable")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		apiFailure(w, r, http.StatusBadRequest, "the token id must be a positive number")
		return
	}
	if err := s.cfg.Store.RevokeAPIToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiFailure(w, r, http.StatusNotFound, "not found")
			return
		}
		apiFailure(w, r, http.StatusInternalServerError, "cannot revoke the token")
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

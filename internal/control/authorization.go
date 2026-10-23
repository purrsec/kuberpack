package control

import (
	"context"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/store"
)

// Principal est l'identité attachée à une requête API. Deux sources :
// le jeton global KUBERPACK_API_TOKEN, qui reste administrateur, et un jeton
// de la table api_tokens, limité à un projet.
type Principal struct {
	Role    string
	Project string
	TokenID int64
	Name    string
}

type principalContextKey struct{}

// withPrincipal attache l'identité au contexte de la requête.
func withPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// principalFrom relit l'identité. L'absence d'identité est un refus, jamais un
// accès administrateur implicite.
func principalFrom(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

// isAdmin dit si le principal administre toute la plateforme.
func (p Principal) isAdmin() bool {
	return p.Role == store.APITokenRoleAdmin
}

// allowsProject dit si le principal agit sur ce projet. Un jeton de projet ne
// vaut que pour son projet, quelle que soit la forme du nom reçue.
func (p Principal) allowsProject(project string) bool {
	if p.isAdmin() {
		return true
	}
	if p.Role != store.APITokenRoleProject || p.Project == "" {
		return false
	}
	return sanitizeName(project) == p.Project
}

// allowsApp dit si le principal peut lire ou piloter cette application. Une
// application hors projet n'appartient à aucun jeton de projet : elle reste
// réservée à l'administrateur.
func (p Principal) allowsApp(app store.App) bool {
	if p.isAdmin() {
		return true
	}
	if p.Role != store.APITokenRoleProject || p.Project == "" {
		return false
	}
	return app.Project != "" && sanitizeName(app.Project) == p.Project
}

// requireAdmin refuse une route réservée à l'administration.
func requireAdmin(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, ok := principalFrom(r.Context())
	if !ok || !principal.isAdmin() {
		http.Error(w, "forbidden: an administrator token is required", http.StatusForbidden)
		return Principal{}, false
	}
	return principal, true
}

// requireProject refuse une opération sur un autre projet. Le message nomme le
// périmètre du jeton : l'appelant sait pourquoi il est refusé, sans découvrir
// l'existence d'une ressource qu'il ne peut pas voir.
func requireProject(w http.ResponseWriter, r *http.Request, project string) (Principal, bool) {
	principal, ok := principalFrom(r.Context())
	if !ok {
		http.Error(w, "forbidden: an administrator token is required", http.StatusForbidden)
		return Principal{}, false
	}
	if !principal.allowsProject(project) {
		http.Error(w, "forbidden: this token is scoped to project "+principal.Project, http.StatusForbidden)
		return Principal{}, false
	}
	return principal, true
}

// appNotFound masque une application d'un autre projet : un jeton de projet ne
// doit pas pouvoir énumérer les noms des autres projets.
func appNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

// filterApps garde les applications visibles par le principal.
func filterApps(principal Principal, apps []store.App) []store.App {
	if principal.isAdmin() {
		return apps
	}
	out := make([]store.App, 0, len(apps))
	for _, app := range apps {
		if principal.allowsApp(app) {
			out = append(out, app)
		}
	}
	return out
}

// authorizationCredential lit le jeton porté par l'en-tête Authorization.
// Bearer est la forme documentée ; token reste accepté pour les anciens
// clients, comme avant l'ajout des jetons de projet.
func authorizationCredential(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	for _, prefix := range []string{"Bearer ", "bearer ", "token "} {
		if strings.HasPrefix(header, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(header, prefix))
		}
	}
	return header
}

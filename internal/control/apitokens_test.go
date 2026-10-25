package control

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func tokenTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kuberpack.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func tokenTestRequest(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func tokenTestSecret(t *testing.T, st *store.Store, name, project string) (store.APIToken, string) {
	t.Helper()
	role := store.APITokenRoleAdmin
	if project != "" {
		role = store.APITokenRoleProject
	}
	token, secret, err := st.CreateAPIToken(context.Background(), name, project, role, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return token, secret
}

func seedTokenApps(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, app := range []store.App{
		{Name: "demo-web", Project: "demo", Namespace: "demo", ForgejoRepository: "demo/web", ProductionBranch: "main", Strategy: "auto", Hostname: "web.demo.example.org", Port: 8080, Healthcheck: "/"},
		{Name: "other-api", Project: "other", Namespace: "other", ForgejoRepository: "other/api", ProductionBranch: "main", Strategy: "auto", Hostname: "api.other.example.org", Port: 8080, Healthcheck: "/"},
		{Name: "legacy", Namespace: "legacy", ForgejoRepository: "legacy/app", ProductionBranch: "main", Strategy: "auto", Hostname: "legacy.example.org", Port: 8080, Healthcheck: "/"},
	} {
		if _, err := st.InsertApp(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SyncImageApps(ctx, "other", []store.App{
		{Name: "other-image", Project: "other", Namespace: "other", Image: "docker.io/library/nginx:1", Hostname: "image.other.example.org"},
	}); err != nil {
		t.Fatal(err)
	}
}

// appNames lit la liste des applications renvoyée par l'API.
func appNames(t *testing.T, response *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatalf("apps response %q: %v", response.Body.String(), err)
	}
	names := make([]string, 0, len(out.Apps))
	for _, app := range out.Apps {
		names = append(names, app.Name)
	}
	return names
}

func projectNames(t *testing.T, response *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Projects []struct {
			Project string `json:"project"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatalf("projects response %q: %v", response.Body.String(), err)
	}
	names := make([]string, 0, len(out.Projects))
	for _, project := range out.Projects {
		names = append(names, project.Project)
	}
	return names
}

func TestAPITokenCreationIsAdminOnlyAndTheSecretIsShownOnce(t *testing.T) {
	st := tokenTestStore(t)
	server := New(Config{Store: st, APIToken: "root"})
	handler := server.Handler()

	var logs bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLog) })

	response := tokenTestRequest(t, handler, http.MethodPost, "/api/v1/tokens", "root",
		`{"name":"ci","role":"deployer","project":"demo","expires_in":"720h"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Project   string `json:"project"`
		Role      string `json:"role"`
		ExpiresAt string `json:"expires_at"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "ci" || created.Project != "demo" || created.Role != store.APITokenRoleDeployer {
		t.Fatalf("unexpected token: %+v", created)
	}
	if !strings.HasPrefix(created.Token, "kp_") || created.ExpiresAt == "" {
		t.Fatalf("unexpected secret or expiration: %+v", created)
	}
	if strings.Contains(response.Body.String(), "token_hash") {
		t.Fatalf("the response leaks the hash: %s", response.Body.String())
	}

	// Le jeton fonctionne immédiatement, mais pas pour administrer les jetons.
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps", created.Token, ""); response.Code != http.StatusOK {
		t.Fatalf("project token on apps: %d %s", response.Code, response.Body.String())
	}
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/tokens", ""},
		{http.MethodPost, "/api/v1/tokens", `{"name":"escalation"}`},
		{http.MethodDelete, "/api/v1/tokens/1", ""},
	} {
		response := tokenTestRequest(t, handler, request.method, request.path, created.Token, request.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s with a project token: %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}

	// La liste administrateur ne contient ni empreinte ni secret.
	response = tokenTestRequest(t, handler, http.MethodGet, "/api/v1/tokens", "root", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	listed := response.Body.String()
	if strings.Contains(listed, "token_hash") || strings.Contains(listed, created.Token) || !strings.Contains(listed, `"name":"ci"`) {
		t.Fatalf("the list leaks a secret: %s", listed)
	}

	// Aucun journal ne contient le secret, même après usage.
	if strings.Contains(logs.String(), created.Token) {
		t.Fatalf("the token was logged: %s", logs.String())
	}

	// La révocation coupe l'accès immédiatement et reste idempotente.
	response = tokenTestRequest(t, handler, http.MethodDelete, "/api/v1/tokens/"+itoa(created.ID), "root", "")
	if response.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps", created.Token, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d %s", response.Code, response.Body.String())
	}
	if response := tokenTestRequest(t, handler, http.MethodDelete, "/api/v1/tokens/"+itoa(created.ID), "root", ""); response.Code != http.StatusOK {
		t.Fatalf("second revoke: %d %s", response.Code, response.Body.String())
	}
	response = tokenTestRequest(t, handler, http.MethodGet, "/api/v1/tokens", "root", "")
	if !strings.Contains(response.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoked token is not listed as revoked: %s", response.Body.String())
	}

	// Le jeton global reste administrateur.
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/tokens", "root", ""); response.Code != http.StatusOK {
		t.Fatalf("global token is no longer an administrator: %d", response.Code)
	}
	// Un jeton inconnu est refusé.
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps", "kp_999_"+strings.Repeat("a", 64), ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d %s", response.Code, response.Body.String())
	}

	// Un rôle admin explicite sans projet peut administrer
	// les jetons comme le jeton global.
	response = tokenTestRequest(t, handler, http.MethodPost, "/api/v1/tokens", "root", `{"name":"deploy","role":"admin","expires_in":"24h"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create administrator token: %d %s", response.Code, response.Body.String())
	}
	var admin struct {
		Role  string `json:"role"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if admin.Role != store.APITokenRoleAdmin || admin.Token == "" {
		t.Fatalf("unexpected administrator token: %+v", admin)
	}
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/tokens", admin.Token, ""); response.Code != http.StatusOK {
		t.Fatalf("administrator token on tokens: %d %s", response.Code, response.Body.String())
	}
	// Un nom de projet doit déjà être normalisé.
	if response := tokenTestRequest(t, handler, http.MethodPost, "/api/v1/tokens", "root", `{"name":"bad","project":"Demo"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid project: %d %s", response.Code, response.Body.String())
	}
}

func TestProjectTokenFiltersAppsBuildsAndLogs(t *testing.T) {
	st := tokenTestStore(t)
	seedTokenApps(t, st)
	_, demo := tokenTestSecret(t, st, "demo", "demo")
	_, other := tokenTestSecret(t, st, "other", "other")
	_, admin := tokenTestSecret(t, st, "admin", "")
	server := New(Config{Store: st, APIToken: "root"})
	handler := server.Handler()

	for _, tc := range []struct {
		name   string
		token  string
		expect []string
	}{
		{"project demo", demo, []string{"demo-web"}},
		{"project other", other, []string{"other-api", "other-image"}},
		{"administrator token", admin, []string{"demo-web", "legacy", "other-api", "other-image"}},
		{"global administrator", "root", []string{"demo-web", "legacy", "other-api", "other-image"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps", tc.token, "")
			if response.Code != http.StatusOK {
				t.Fatalf("list: %d %s", response.Code, response.Body.String())
			}
			got := appNames(t, response)
			if strings.Join(got, ",") != strings.Join(tc.expect, ",") {
				t.Fatalf("apps %v, want %v", got, tc.expect)
			}
		})
	}

	// Les applications d'un autre projet ou hors projet sont invisibles.
	for _, path := range []string{
		"/api/v1/apps/other-api",
		"/api/v1/apps/other-api/status",
		"/api/v1/apps/other-api/builds",
		"/api/v1/apps/other-api/builds/1",
		"/api/v1/apps/other-api/builds/1/logs",
		"/api/v1/apps/other-api/logs",
		"/api/v1/apps/other-image",
		"/api/v1/apps/other-image/builds",
		"/api/v1/apps/legacy",
	} {
		response := tokenTestRequest(t, handler, http.MethodGet, path, demo, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s with a demo token: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := tokenTestRequest(t, handler, http.MethodPost, "/api/v1/apps/other-api/redeploy", demo, "")
	if response.Code != http.StatusGone {
		t.Fatalf("retired redeploy: %d %s", response.Code, response.Body.String())
	}

	// Le même jeton lit son application et ses builds.
	for _, path := range []string{"/api/v1/apps/demo-web", "/api/v1/apps/demo-web/status", "/api/v1/apps/demo-web/builds"} {
		response := tokenTestRequest(t, handler, http.MethodGet, path, demo, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s with its own token: %d %s", path, response.Code, response.Body.String())
		}
	}
	// L'administrateur garde la vue complète.
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps/other-api", "root", ""); response.Code != http.StatusOK {
		t.Fatalf("administrator get: %d %s", response.Code, response.Body.String())
	}
	// Sans credential, la route reste fermée.
	if response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/apps", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", response.Code, response.Body.String())
	}
}

func TestLegacyWritesCannotBypassPlans(t *testing.T) {
	st := tokenTestStore(t)
	_, demo := tokenTestSecret(t, st, "demo", "demo")
	server := New(Config{Store: st, APIToken: "root"})
	handler := server.Handler()

	// GitOps n'est pas configuré : une requête autorisée atteint ce refus,
	// une requête hors périmètre est arrêtée avant par l'autorisation.
	for _, tc := range []struct {
		method, path, body, token string
		status                    int
		message                   string
	}{
		{http.MethodPost, "/api/v1/projects", `{"project":"demo"}`, demo, http.StatusForbidden, "administrator token is required"},
		{http.MethodPost, "/api/v1/projects", `{"project":"other"}`, demo, http.StatusForbidden, "administrator token is required"},
		{http.MethodPost, "/api/v1/projects", `{"project":"demo"}`, "root", http.StatusGone, "upgrade_required"},
		{http.MethodDelete, "/api/v1/projects/demo", "", demo, http.StatusForbidden, "administrator token is required"},
		{http.MethodDelete, "/api/v1/projects/other", "", demo, http.StatusForbidden, "administrator token is required"},
		{http.MethodDelete, "/api/v1/projects/demo", "", "root", http.StatusGone, "upgrade_required"},
		{http.MethodGet, "/api/v1/projects/demo", "", demo, http.StatusForbidden, "gitops is not configured"},
		{http.MethodGet, "/api/v1/projects/other", "", demo, http.StatusForbidden, "scoped to project demo"},
	} {
		response := tokenTestRequest(t, handler, tc.method, tc.path, tc.token, tc.body)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.message) {
			t.Fatalf("%s %s token %s: %d %q, want %d %q",
				tc.method, tc.path, tc.token, response.Code, strings.TrimSpace(response.Body.String()), tc.status, tc.message)
		}
	}
}

func TestProjectListIsFilteredByToken(t *testing.T) {
	bare := setupControlProjectsRepo(t)
	st := tokenTestStore(t)
	_, demo := tokenTestSecret(t, st, "demo", "demo")
	_, other := tokenTestSecret(t, st, "other", "other")
	_, admin := tokenTestSecret(t, st, "admin", "")
	server := New(Config{
		Store:        st,
		APIToken:     "root",
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		Provider:     mustProvider(t, "https://git.example.org", nil),
		Platform:     promote.Platform{},
	})
	handler := server.Handler()

	for _, tc := range []struct {
		name   string
		token  string
		expect []string
	}{
		{"project demo", demo, []string{"demo"}},
		{"project other", other, []string{"other"}},
		{"administrator token", admin, []string{"demo", "other"}},
		{"global administrator", "root", []string{"demo", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := tokenTestRequest(t, handler, http.MethodGet, "/api/v1/projects", tc.token, "")
			if response.Code != http.StatusOK {
				t.Fatalf("list: %d %s", response.Code, response.Body.String())
			}
			got := projectNames(t, response)
			if strings.Join(got, ",") != strings.Join(tc.expect, ",") {
				t.Fatalf("projects %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestExpiredProjectTokenIsRejected(t *testing.T) {
	st := tokenTestStore(t)
	token, secret, err := st.CreateAPIToken(context.Background(), "short", "demo", store.APITokenRoleProject, time.Now().Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if token.ID == 0 {
		t.Fatal("no token id")
	}
	time.Sleep(250 * time.Millisecond)
	server := New(Config{Store: st, APIToken: "root"})
	if response := tokenTestRequest(t, server.Handler(), http.MethodGet, "/api/v1/apps", secret, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d %s", response.Code, response.Body.String())
	}
}

func setupControlProjectsRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for the project list")
	}
	dir := t.TempDir()
	bare := filepath.Join(dir, "gitops.git")
	seed := filepath.Join(dir, "seed")
	controlGit(t, "", "init", "--bare", "-b", "main", bare)
	controlGit(t, "", "clone", bare, seed)
	for _, project := range []string{"demo", "other"} {
		projectDir := filepath.Join(seed, "kubernetes", "vps", "projects", project)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := "project: " + project + "\nnamespace: " + project + "\n"
		if err := os.WriteFile(filepath.Join(projectDir, "kuberpack.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	controlGit(t, seed, "add", ".")
	controlGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.org", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	controlGit(t, seed, "push", "-u", "origin", "HEAD:main")
	return bare
}

func controlGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

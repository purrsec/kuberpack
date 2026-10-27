package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
)

func testApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	// Interactive fixtures model a developer terminal independently of the
	// runner environment. Tests of CI behaviour opt in explicitly below.
	for _, name := range []string{"CI", "KUBERPACK_API_URL", "KUBERPACK_API_TOKEN"} {
		t.Setenv(name, "")
	}
	var out, err bytes.Buffer
	return &App{In: strings.NewReader(""), Out: &out, Err: &err, WorkDir: t.TempDir(), ConfigDir: t.TempDir()}, &out, &err
}
func tlsFixture(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return s, ca
}
func writeConnection(t *testing.T, a *App, origin, ca string) {
	t.Helper()
	a.config = configuration{Current: "test", Contexts: map[string]connection{"test": {URL: origin, Store: "file", CAFile: ca}}, Links: map[string]association{}}
	if err := a.saveCredential("test", a.config.Contexts["test"], "test-project-token"); err != nil {
		t.Fatal(err)
	}
	if err := a.saveConfig(); err != nil {
		t.Fatal(err)
	}
}
func session() api.Session {
	return api.Session{Version: "2", Name: "developer", ForgeURL: "https://git.example.org", Role: "maintainer", Project: "demo", Capabilities: []string{"plans", "deployments", "idempotency", "scoped_roles", "configuration_observation"}}
}

func TestPublicCLIHasNoRuntimeOrOperatorCommands(t *testing.T) {
	for _, name := range []string{"serve", "server", "builder", "reloader", "trigger", "promote", "install", "doctor", "token"} {
		a, _, _ := testApp(t)
		if err := a.Run(context.Background(), []string{name}); err == nil || Code(err) != 2 {
			t.Fatalf("%s unexpectedly executable: %v", name, err)
		}
	}
	a, out, _ := testApp(t)
	if err := a.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wizard") || strings.Contains(out.String(), "\n  serve ") {
		t.Fatalf("unexpected help: %s", out)
	}
}
func TestWizardRefusesMachineModesAndDoesNotReadInput(t *testing.T) {
	for _, args := range [][]string{{"wizard"}, {"wizard", "--non-interactive"}, {"wizard", "--output", "json"}, {"wizard", "--yes"}} {
		a, _, _ := testApp(t)
		if err := a.Run(context.Background(), args); err == nil || Code(err) != 2 {
			t.Fatalf("wizard accepted machine mode %v: %v", args, err)
		}
	}
}

func TestWizardRefusesCIOnAnInteractiveTerminal(t *testing.T) {
	a, _, _ := testApp(t)
	a.Interactive = true
	t.Setenv("CI", "true")
	if err := a.Run(context.Background(), []string{"wizard"}); err == nil || Code(err) != 2 {
		t.Fatalf("wizard accepted a CI terminal: %v", err)
	}
}
func TestLoginVerifiesAndPersistsPrivateCredentialWithoutPrintingIt(t *testing.T) {
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/session" || r.Header.Get("Authorization") != "Bearer login-secret" {
			t.Error("invalid login request")
		}
		_ = json.NewEncoder(w).Encode(session())
	}))
	a, out, _ := testApp(t)
	a.In = strings.NewReader("login-secret\n")
	err := a.Run(context.Background(), []string{"login", "--url", s.URL, "--context", "test", "--ca-file", ca, "--credential-store", "file", "--token-stdin", "--output", "json"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "login-secret") {
		t.Fatal("login printed the credential")
	}
	data, err := os.ReadFile(filepath.Join(a.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "login-secret") {
		t.Fatal("configuration contains credential")
	}
	st, err := os.Stat(a.credentialFile("test"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v %v", st, err)
	}
}
func TestPartialEnvironmentNeverBorrowsSavedCredential(t *testing.T) {
	a, _, _ := testApp(t)
	t.Setenv("KUBERPACK_API_URL", "https://other.example.com")
	t.Setenv("KUBERPACK_API_TOKEN", "")
	if err := a.Run(context.Background(), []string{"whoami"}); err == nil || Code(err) != 2 {
		t.Fatalf("partial environment accepted: %v", err)
	}
}
func TestAuthenticatedRedirectsAreRefused(t *testing.T) {
	var received bool
	target, _ := tlsFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received = true }))
	origin, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	c, err := newClient(origin.URL, "secret", ca)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.json(context.Background(), http.MethodGet, "/api/v2/session", nil, new(any)); err == nil {
		t.Fatal("redirect followed")
	}
	if received {
		t.Fatal("credential sent to redirect target")
	}
}

func TestGuidedFirstDeploymentNeedsNoFlagsOrPrematureServiceLink(t *testing.T) {
	var planCount, deployCount int
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/session":
			_ = json.NewEncoder(w).Encode(session())
		case "/api/v2/projects/demo":
			p := api.Project{Name: "demo", Namespace: "demo", Services: []api.Service{}}
			if deployCount > 0 {
				p.Services = []api.Service{{ID: "demo-web", Name: "web", Project: "demo"}}
			}
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v2/projects/demo/plans":
			planCount++
			var request api.PlanRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.ExpectedRevision == nil || *request.ExpectedRevision != "" {
				t.Error("first deploy must assert an empty base")
			}
			if !strings.Contains(request.Manifest, "repository: acme/api") {
				t.Errorf("wrong proposed source: %s", request.Manifest)
			}
			_ = json.NewEncoder(w).Encode(api.Plan{ID: "plan_1", Project: "demo", Action: "deploy", Changes: []api.Change{{Resource: "service/web", Action: "add"}}})
		case "/api/v2/projects/demo/deployments":
			deployCount++
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(api.Deployment{ID: "dep_1", Project: "demo", Status: "accepted"})
		case "/api/v2/projects/demo/deployments/dep_1":
			_ = json.NewEncoder(w).Encode(api.Deployment{ID: "dep_1", Project: "demo", Status: "succeeded"})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	a, _, diagnostics := testApp(t)
	writeConnection(t, a, s.URL, ca)
	a.Interactive = true
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://git.example.org/acme/api.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.WorkDir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, b)
		}
	}
	// Task, source kind, detected repository default, port, missing hostname, grouped local recap, final remote plan.
	a.In = strings.NewReader("1\n1\n\n\ndemo.example.org\n1\ny\n")
	if err := a.Run(context.Background(), nil); err != nil {
		t.Fatalf("wizard: %v\n%s", err, diagnostics)
	}
	if planCount != 1 || deployCount != 1 {
		t.Fatalf("unexpected requests: plan=%d deploy=%d", planCount, deployCount)
	}
	if strings.Count(diagnostics.String(), "Deploy this exact plan?") != 1 {
		t.Fatalf("redundant or missing remote confirmation: %s", diagnostics)
	}
	if _, err := os.Stat(filepath.Join(a.WorkDir, "kuberpack.yaml")); err != nil {
		t.Fatal(err)
	}
	if a.config.Links[a.WorkDir].Service != "web" {
		t.Fatal("the API-registered service was not remembered")
	}
}

func TestInitExistingProjectNeverGeneratesAReplacementFragment(t *testing.T) {
	full := "project: demo\nservices:\n  one:\n    repository: acme/one\n  two:\n    repository: acme/two\n"
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/session" {
			_ = json.NewEncoder(w).Encode(session())
			return
		}
		_ = json.NewEncoder(w).Encode(api.Project{Name: "demo", Revision: "base", Manifest: full, Services: []api.Service{{Name: "one"}, {Name: "two"}}})
	}))
	a, _, _ := testApp(t)
	writeConnection(t, a, s.URL, ca)
	if err := a.Run(context.Background(), []string{"init", "--yes"}); err == nil {
		t.Fatal("existing project was replaced by a draft")
	}
	if _, err := os.Stat(filepath.Join(a.WorkDir, "kuberpack.yaml")); !os.IsNotExist(err) {
		t.Fatal("unexpected local manifest")
	}
	b, _, _ := testApp(t)
	b.ConfigDir = a.ConfigDir
	if err := b.Run(context.Background(), []string{"init", "--from-remote", "--yes"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(b.WorkDir, "kuberpack.yaml"))
	if err != nil || string(data) != full {
		t.Fatalf("complete manifest was not preserved: %s %v", data, err)
	}
}

func TestJSONPlanNeverConfirmsOrExecutes(t *testing.T) {
	var writes int
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/session":
			_ = json.NewEncoder(w).Encode(session())
		case "/api/v2/projects/demo/plans":
			_ = json.NewEncoder(w).Encode(api.Plan{ID: "plan_1", Project: "demo"})
		default:
			writes++
			http.NotFound(w, r)
		}
	}))
	a, out, _ := testApp(t)
	writeConnection(t, a, s.URL, ca)
	if err := os.WriteFile(filepath.Join(a.WorkDir, "kuberpack.yaml"), []byte("project: demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), []string{"deploy", "--plan", "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("plan executed a mutation")
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("stdout is not one JSON document: %s", out)
	}
}

func TestWaitFailureAndTimeoutHaveDistinctExitCodes(t *testing.T) {
	for _, status := range []string{"failed", "deploying"} {
		t.Run(status, func(t *testing.T) {
			s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/session":
					_ = json.NewEncoder(w).Encode(session())
				default:
					_ = json.NewEncoder(w).Encode(api.Deployment{ID: "dep_1", Project: "demo", Status: status})
				}
			}))
			a, out, _ := testApp(t)
			writeConnection(t, a, s.URL, ca)
			err := a.Run(context.Background(), []string{"deployments", "wait", "dep_1", "--timeout", "10ms", "--output", "json"})
			want := 5
			if status == "deploying" {
				want = 6
			}
			if Code(err) != want {
				t.Fatalf("%s: code %d, want %d: %v", status, Code(err), want, err)
			}
			a.reportError(err)
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("stdout is not one JSON document: %s", out)
			}
			if result["error"] == nil {
				t.Fatalf("failure has no structured error: %s", out)
			}
			if status == "failed" {
				deployment, ok := result["result"].(map[string]any)
				if !ok || deployment["deployment_id"] != "dep_1" {
					t.Fatalf("failure lost its deployment identifier: %s", out)
				}
			}
		})
	}
}

func TestInitWithDetectedRemoteCanDeployAPublishedImage(t *testing.T) {
	a, _, _ := testApp(t)
	a.Interactive = true
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://git.example.org/acme/api.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.WorkDir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, b)
		}
	}
	// Project, published image source, image reference, default port,
	// hostname, save the proposed configuration.
	a.In = strings.NewReader("demo\n2\nghcr.io/immich:release\n\ndemo.example.org\n1\n")
	if err := a.Run(context.Background(), []string{"init"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "kuberpack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "image: ghcr.io/immich:release") {
		t.Fatalf("manifest does not deploy the image: %s", data)
	}
	if strings.Contains(string(data), "repository:") {
		t.Fatalf("manifest kept the detected repository: %s", data)
	}
}

func TestInitWithDetectedRemoteDefaultsToTheRemoteRepository(t *testing.T) {
	a, _, _ := testApp(t)
	a.Interactive = true
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://git.example.org/acme/api.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.WorkDir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, b)
		}
	}
	// Project, git source, detected repository default, default port,
	// hostname, save the proposed configuration.
	a.In = strings.NewReader("demo\n1\n\n\ndemo.example.org\n1\n")
	if err := a.Run(context.Background(), []string{"init"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "kuberpack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "repository: ") || !strings.Contains(string(data), "acme/api") {
		t.Fatalf("manifest does not reuse the detected remote: %s", data)
	}
	if strings.Contains(string(data), "image:") {
		t.Fatalf("manifest gained an image: %s", data)
	}
}

func TestLocalFilesAreNotOverwritten(t *testing.T) {
	a, _, _ := testApp(t)
	name := filepath.Join(a.WorkDir, "kuberpack.yaml")
	if err := os.WriteFile(name, []byte("keep-me"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), []string{"init"}); err == nil {
		t.Fatal("existing manifest overwritten")
	}
	b, _ := os.ReadFile(name)
	if string(b) != "keep-me" {
		t.Fatal("manifest changed")
	}
}
func TestOutputSanitizesTerminalControlsAndCredential(t *testing.T) {
	if v := clean("\x1b[31msecret\x1b[0m\x00", "secret"); v != "[redacted]" {
		t.Fatalf("unsafe output: %q", v)
	}
}

func TestRepeatingRequestIDResumesWithoutPlanningOrDeployingAgain(t *testing.T) {
	var previous *api.Deployment
	plans, executions := 0, 0
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/session":
			_ = json.NewEncoder(w).Encode(session())
		case "/api/v2/projects/demo/requests/job-42":
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment": previous})
		case "/api/v2/projects/demo/plans":
			plans++
			_ = json.NewEncoder(w).Encode(api.Plan{ID: "plan_42", Project: "demo"})
		case "/api/v2/projects/demo/deployments":
			executions++
			var body api.DeploymentRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			previous = &api.Deployment{ID: "dep_42", Project: "demo", Status: "accepted", RequestFingerprint: body.Fingerprint}
			_ = json.NewEncoder(w).Encode(previous)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	a, _, _ := testApp(t)
	writeConnection(t, a, s.URL, ca)
	file := filepath.Join(a.WorkDir, "kuberpack.yaml")
	if err := os.WriteFile(file, []byte("project: demo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args := []string{"deploy", "--yes", "--detach", "--request-id", "job-42", "--output", "json"}
	if err := a.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	b, _, _ := testApp(t)
	b.ConfigDir = a.ConfigDir
	b.WorkDir = a.WorkDir
	if err := b.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if plans != 1 || executions != 1 {
		t.Fatalf("request was repeated: plans=%d executions=%d", plans, executions)
	}
	if err := os.WriteFile(file, []byte("project: demo\nservices: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, _, _ := testApp(t)
	c.ConfigDir = a.ConfigDir
	c.WorkDir = a.WorkDir
	if err := c.Run(context.Background(), args); err == nil {
		t.Fatal("request ID accepted different content")
	}
}

func TestCtrlCInterruptsAnUnfinishedFormWithoutSaving(t *testing.T) {
	a, _, _ := testApp(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	a.In = reader
	a.Interactive = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, []string{"init"}) }()
	cancel()
	select {
	case err := <-done:
		if Code(err) != 130 {
			t.Fatalf("interrupt code: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt did not respond to cancellation")
	}
	if _, err := os.Stat(filepath.Join(a.WorkDir, "kuberpack.yaml")); !os.IsNotExist(err) {
		t.Fatal("interrupted form was saved")
	}
}

func TestBuildLogFollowPinsLatestIDAndReadsFinalSnapshot(t *testing.T) {
	listReads, logReads := 0, 0
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/session":
			_ = json.NewEncoder(w).Encode(session())
		case "/api/v2/projects/demo/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []api.Service{{ID: "demo-web", Name: "web", Project: "demo", Repository: "acme/api"}}})
		case "/api/v1/apps/demo-web/builds":
			listReads++
			_ = json.NewEncoder(w).Encode(map[string]any{"builds": []map[string]int{{"id": 42}}})
		case "/api/v1/apps/demo-web/builds/42/logs":
			logReads++
			if logReads == 1 {
				_, _ = w.Write([]byte("starting\n"))
			} else {
				_, _ = w.Write([]byte("starting\ndone\n"))
			}
		case "/api/v1/apps/demo-web/builds/42":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
		default:
			t.Errorf("unexpected log target %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	a, out, _ := testApp(t)
	writeConnection(t, a, s.URL, ca)
	if err := a.Run(context.Background(), []string{"builds", "logs", "--latest", "--follow"}); err != nil {
		t.Fatal(err)
	}
	if listReads != 1 || logReads != 2 || out.String() != "starting\ndone\n" {
		t.Fatalf("log follow lost or repeated content: latest=%d snapshots=%d output=%q", listReads, logReads, out.String())
	}
}

// A git folder does not force the wizard onto its own remote: the guided
// first deployment can choose a published image instead.
func TestWizardInGitFolderCanDeployAPublishedImage(t *testing.T) {
	sess := session()
	sess.Project = "immich"
	s, ca := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/session":
			_ = json.NewEncoder(w).Encode(sess)
		case "/api/v2/projects/immich":
			_ = json.NewEncoder(w).Encode(api.Project{Name: "immich", Revision: "rev-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	a, _, _ := testApp(t)
	writeConnection(t, a, s.URL, ca)
	a.Interactive = true
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://git.example.org/acme/api.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.WorkDir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, b)
		}
	}
	// Task 1 (prepare and deploy), source 2 (published image), image
	// reference, default port, hostname, save. The plan request then 404s:
	// this fixture proves the guided manifest, not the deployment.
	a.In = strings.NewReader("1\n2\nghcr.io/immich-app/immich-server:release\n\nphotos.example.org\n1\n")
	if err := a.Run(context.Background(), nil); err == nil {
		t.Fatal("wizard deployed without a plan endpoint")
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "kuberpack.yaml"))
	if err != nil {
		t.Fatalf("wizard wrote no manifest: %v", err)
	}
	if !strings.Contains(string(data), "image: ghcr.io/immich-app/immich-server:release") {
		t.Fatalf("wizard did not take the published image: %s", data)
	}
	if strings.Contains(string(data), "repository:") {
		t.Fatalf("wizard kept the detected repository: %s", data)
	}
}

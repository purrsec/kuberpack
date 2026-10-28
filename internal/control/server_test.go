package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	gitpkg "git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func TestWebhookHMACAndQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	secrets := store.SecretsDir(filepath.Join(dir, "secrets"))
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name:              "hello-world",
		ForgejoRepository: "pepe/hello-world",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		Hostname:          "hello-world.host.bzh",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(app.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}

	statuses := make(chan map[string]string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("status body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		statuses <- body
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())

	ran := make(chan string, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Provider: provider,
		APIToken: "api",
		Run: func(ctx context.Context, req release.Request) (release.Result, error) {
			ran <- req.SHA
			ref, err := image.Pin("git.host.bzh/pepe/hello-world", req.SHA, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			if err != nil {
				return release.Result{}, err
			}
			return release.Result{SHA: req.SHA, Image: ref, InfraCommitSHA: "infra"}, nil
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"pepe/hello-world"}}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "del-42")
	req.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}

	select {
	case sha := <-ran:
		if sha != "0123456789abcdef0123456789abcdef01234567" {
			t.Fatalf("sha %q", sha)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run")
	}

	got := map[string]int{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case st := <-statuses:
			got[st["state"]]++
			if st["context"] != forgejo.ProductionContext {
				t.Fatalf("context %q", st["context"])
			}
			if st["target_url"] != "https://hello-world.host.bzh" {
				t.Fatalf("target_url %q", st["target_url"])
			}
		case <-deadline:
			t.Fatalf("commit statuses: %v", got)
		}
	}
	if got[forgejo.StatusPending] < 1 || got[forgejo.StatusSuccess] != 1 {
		t.Fatalf("commit statuses: %v", got)
	}

	bad := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	bad.Header.Set("X-Gitea-Event", "push")
	bad.Header.Set("X-Gitea-Delivery", "del-43")
	bad.Header.Set("X-Gitea-Signature", "deadbeef")
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad hmac: %d", badRec.Code)
	}

	dup := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	dup.Header.Set("X-Gitea-Event", "push")
	dup.Header.Set("X-Gitea-Delivery", "del-42")
	dup.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	dupRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(dupRec, dup)
	if dupRec.Code != http.StatusAccepted {
		t.Fatalf("dup %d", dupRec.Code)
	}

	// Forge retries can carry a new delivery identifier. The commit is
	// already built, so another webhook envelope must not rebuild it.
	sameCommit := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	sameCommit.Header.Set("X-Gitea-Event", "push")
	sameCommit.Header.Set("X-Gitea-Delivery", "del-44")
	sameCommit.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	sameCommitRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(sameCommitRec, sameCommit)
	if sameCommitRec.Code != http.StatusAccepted || !strings.Contains(sameCommitRec.Body.String(), "duplicate") {
		t.Fatalf("same commit %d %s", sameCommitRec.Code, sameCommitRec.Body.String())
	}
	builds, err := st.ListBuilds(ctx, app.ID)
	if err != nil || len(builds) != 1 {
		t.Fatalf("same commit queued another build: %+v %v", builds, err)
	}
}

func TestFrozenTrackSkipsPromote(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	secrets := store.SecretsDir(filepath.Join(dir, "secrets"))
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name:              "hello-world",
		ForgejoRepository: "pepe/hello-world",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		Hostname:          "hello-world.host.bzh",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(app.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())

	skip := make(chan bool, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Provider: provider,
		APIToken: "api",
		GitOpsFile: func(context.Context, store.App) ([]byte, error) {
			return []byte(`track: f10cf296ea2c210d374847d1368d0ef9c848664c
image: "git.host.bzh/pepe/hello-world:sha-f10cf296ea2c210d374847d1368d0ef9c848664c@sha256:127130f2edce05a906342bba4700c43b9718a6fb81891b6af62398308d92de02"
`), nil
		},
		Run: func(ctx context.Context, req release.Request) (release.Result, error) {
			skip <- req.SkipPromote
			ref, err := image.Pin("git.host.bzh/pepe/hello-world", req.SHA, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			if err != nil {
				return release.Result{}, err
			}
			return release.Result{SHA: req.SHA, Image: ref}, nil
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"pepe/hello-world"}}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "del-frozen")
	req.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-skip:
		if !got {
			t.Fatal("expected SkipPromote")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run")
	}
}

func TestCreateProjectUnauthorized(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(Config{
		Store:    st,
		Secrets:  store.SecretsDir(filepath.Join(dir, "secrets")),
		APIToken: "api",
		Provider: mustForgejo(t),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader([]byte(`{"project":"web"}`)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

// The manifest is the only way to declare an application: the reconciliation
// creates the app record, the webhook and the first build.
func TestReconcileArmsThePipeline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "pepe/hello-world",
			"clone_url": "https://git.host.bzh/pepe/hello-world.git",
			"name":      "hello-world",
			"owner":     map[string]string{"login": "pepe"},
		})
	})
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":   "main",
			"commit": map[string]string{"id": "0123456789abcdef0123456789abcdef01234567"},
		})
	})
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/hooks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "config": map[string]string{"url": "https://kp.example/hooks/forgejo"}})
	})
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ran := make(chan struct{}, 1)
	srv := New(Config{
		Store:      st,
		Secrets:    store.SecretsDir(filepath.Join(dir, "secrets")),
		Provider:   provider,
		Token:      "tok",
		APIToken:   "api",
		WebhookURL: "https://kp.example/hooks/forgejo",
		Run: func(context.Context, release.Request) (release.Result, error) {
			ran <- struct{}{}
			return release.Result{}, nil
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	ctx := context.Background()
	declared := []promote.ProjectApp{{
		Name:        "demo-hello-world",
		Service:     "hello-world",
		Repository:  "pepe/hello-world",
		Branch:      "main",
		Strategy:    "auto",
		Hostname:    "hello-world.host.bzh",
		Port:        8080,
		Healthcheck: "/healthz",
		ValuesPath:  "kubernetes/vps/projects/demo/hello-world/values.yaml",
	}}
	if err := srv.reconcileApps(ctx, "demo", declared); err != nil {
		t.Fatal(err)
	}

	app, err := st.AppByName(ctx, "demo-hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if app.Project != "demo" {
		t.Fatalf("project: %q", app.Project)
	}
	if app.ValuesPath != "kubernetes/vps/projects/demo/hello-world/values.yaml" {
		t.Fatalf("values path: %q", app.ValuesPath)
	}
	if app.WebhookID != 9 {
		t.Fatalf("webhook: %d", app.WebhookID)
	}
	if app.Healthcheck != "/healthz" || app.Port != 8080 {
		t.Fatalf("runtime: %+v", app)
	}
	if !app.Autodeploy || !app.AutodeployPR {
		t.Fatalf("autodeploy defaults: autodeploy=%v autodeploy_pr=%v", app.Autodeploy, app.AutodeployPR)
	}
	secret, err := store.SecretsDir(filepath.Join(dir, "secrets")).Get(app.ID)
	if err != nil || secret == "" {
		t.Fatalf("the webhook secret must exist: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("first build was not queued")
	}

	// A second reconciliation is a no-op: no new webhook, no new build.
	if err := srv.reconcileApps(ctx, "demo", declared); err != nil {
		t.Fatal(err)
	}
	app, err = st.AppByName(ctx, "demo-hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if app.WebhookID != 9 {
		t.Fatalf("webhook: %d", app.WebhookID)
	}
	apps, err := st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("the reconciliation must not duplicate the app: %d", len(apps))
	}

	// A service that left the manifest loses its app.
	if err := srv.reconcileApps(ctx, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppByName(ctx, "demo-hello-world"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the app must be removed: %v", err)
	}
}

func TestCommitStatusOnBuildFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	secrets := store.SecretsDir(filepath.Join(dir, "secrets"))
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name:              "hello-world",
		ForgejoRepository: "pepe/hello-world",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		Hostname:          "hello-world.host.bzh",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(app.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}

	states := make(chan string, 4)
	targets := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("status body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		states <- body["state"]
		if body["state"] == forgejo.StatusFailure {
			targets <- body["target_url"]
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())

	done := make(chan struct{}, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Provider: provider,
		APIToken: "api",
		Run: func(context.Context, release.Request) (release.Result, error) {
			done <- struct{}{}
			return release.Result{}, errors.New("scan failed")
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"pepe/hello-world"}}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "del-fail")
	req.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run")
	}

	got := map[string]int{}
	deadline := time.After(2 * time.Second)
	for got[forgejo.StatusFailure] < 1 {
		select {
		case state := <-states:
			got[state]++
		case <-deadline:
			t.Fatalf("commit statuses: %v", got)
		}
	}
	select {
	case target := <-targets:
		if !strings.Contains(target, "/pepe/hello-world/commit/0123456789abcdef0123456789abcdef01234567") {
			t.Fatalf("failure target_url %q", target)
		}
	default:
		t.Fatal("missing failure target_url")
	}
}

func TestPullRequestPreviewAndForkDenied(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	secrets := store.SecretsDir(filepath.Join(dir, "secrets"))
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name:              "hello-world",
		ForgejoRepository: "pepe/hello-world",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		AutodeployPR:      true,
		Hostname:          "hello-world.host.bzh",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(app.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}
	contexts := make(chan string, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		contexts <- body["context"]
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())
	ran := make(chan bool, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Provider: provider,
		APIToken: "api",
		Platform: promote.Platform{PreviewDomain: "preview.host.bzh"},
		Run: func(ctx context.Context, req release.Request) (release.Result, error) {
			ran <- req.AllowNonHEAD
			ref, err := image.Pin("git.host.bzh/pepe/hello-world", req.SHA, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			if err != nil {
				return release.Result{}, err
			}
			return release.Result{SHA: req.SHA, Image: ref}, nil
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	body := []byte(`{"action":"opened","number":7,"pull_request":{"number":7,"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"full_name":"pepe/hello-world","fork":false}},"base":{"repo":{"full_name":"pepe/hello-world"}}},"repository":{"full_name":"pepe/hello-world"}}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Delivery", "pr-7")
	req.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	select {
	case allow := <-ran:
		if !allow {
			t.Fatal("preview must skip production HEAD check")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preview worker did not run")
	}
	deadline := time.After(2 * time.Second)
	sawPreview := false
	for !sawPreview {
		select {
		case c := <-contexts:
			if c == forgejo.PreviewContext {
				sawPreview = true
			}
		case <-deadline:
			t.Fatal("missing kuberpack/preview status")
		}
	}

	fork := []byte(`{"action":"opened","number":8,"pull_request":{"number":8,"head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"full_name":"evil/hello-world","fork":true}},"base":{"repo":{"full_name":"pepe/hello-world"}}},"repository":{"full_name":"pepe/hello-world"}}`)
	forkReq := httptest.NewRequest(http.MethodPost, "/hooks/forgejo", bytes.NewReader(fork))
	forkReq.Header.Set("X-Gitea-Event", "pull_request")
	forkReq.Header.Set("X-Gitea-Delivery", "pr-fork")
	forkReq.Header.Set("X-Gitea-Signature", hmacsig.Hex("s3cret", fork))
	forkRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forkRec, forkReq)
	if forkRec.Code != http.StatusNoContent {
		t.Fatalf("fork: %d", forkRec.Code)
	}
}

func TestListBuildsAndRemoveApp(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name: "demo-web", Project: "demo", ForgejoRepository: "pepe/web", ProductionBranch: "main",
		Strategy: "auto", Autodeploy: true, Hostname: "web.example.org", Port: 8080, Healthcheck: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertBuild(ctx, store.Build{AppID: app.ID, Environment: "production", CommitSHA: "abc", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishBuild(ctx, id, store.Build{Status: "succeeded", ImageDigest: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/web/hooks/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())
	srv := New(Config{Store: st, Secrets: store.SecretsDir(filepath.Join(dir, "secrets")), Provider: provider, APIToken: "api"})

	list := httptest.NewRequest(http.MethodGet, "/api/v1/apps/demo-web/builds", nil)
	list.Header.Set("Authorization", "Bearer api")
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"succeeded"`) {
		t.Fatalf("builds %d %s", listRec.Code, listRec.Body.String())
	}

	// A service that left the manifest loses its app: the declaration is
	// the manifest, not the API.
	if err := srv.reconcileApps(ctx, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppByName(ctx, "demo-web"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the app must be removed: %v", err)
	}
}

func TestFluxHookMarksRolledBack(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name: "hello-world", ForgejoRepository: "pepe/hello-world", ProductionBranch: "main",
		Strategy: "auto", Autodeploy: true, Hostname: "hello-world.host.bzh", Port: 8080, Healthcheck: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.InsertBuild(ctx, store.Build{AppID: app.ID, Environment: "production", CommitSHA: "0123456789abcdef0123456789abcdef01234567", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishBuild(ctx, id, store.Build{Status: "succeeded", ImageDigest: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	states := make(chan string, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		states <- body["state"]
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	provider := mustProvider(t, git.URL, git.Client())
	srv := New(Config{Store: st, Provider: provider, APIToken: "api", FluxSecret: "flux-secret"})
	body := []byte(`{"severity":"error","message":"Helm test failed","involvedObject":{"kind":"HelmRelease","name":"hello-world","namespace":"apps"}}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/flux", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer flux-secret")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("flux %d %s", rec.Code, rec.Body.String())
	}
	select {
	case state := <-states:
		if state != forgejo.StatusWarning {
			t.Fatalf("state %s", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing warning status")
	}
	got, err := st.LatestProductionBuild(ctx, app.ID)
	if err != nil || got.Status != "rolled_back" {
		t.Fatalf("%+v %v", got, err)
	}
}

// mustProvider builds a provider that talks to a test server.
func mustProvider(t *testing.T, baseURL string, client *http.Client) gitpkg.Provider {
	t.Helper()
	c, err := forgejo.New(baseURL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if client != nil {
		c.HTTPClient = client
	}
	return &gitpkg.ForgejoProvider{Client: c}
}

func mustForgejo(t *testing.T) gitpkg.Provider {
	t.Helper()
	p, err := gitpkg.NewForgejo("https://git.host.bzh", "tok")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

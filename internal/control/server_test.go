package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/image"
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
	client, err := forgejo.New(git.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = git.Client()

	ran := make(chan string, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Client:   client,
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
}

func TestCreateAppUnauthorized(t *testing.T) {
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
		Client:   mustForgejo(t),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader([]byte(`{"name":"web"}`)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCreateAndGetApp(t *testing.T) {
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
	client, err := forgejo.New(git.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = git.Client()

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
		Client:     client,
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

	body := []byte(`{"name":"hello-world","repository":"pepe/hello-world","hostname":"hello-world.host.bzh","port":8080}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer api")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["webhook_configured"] != true {
		t.Fatalf("%v", created)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/apps/hello-world", nil)
	get.Header.Set("Authorization", "Bearer api")
	getRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get %d", getRec.Code)
	}

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("first build was not queued")
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
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("status body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		states <- body["state"]
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	git := httptest.NewServer(mux)
	t.Cleanup(git.Close)
	client, err := forgejo.New(git.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = git.Client()

	done := make(chan struct{}, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  secrets,
		Client:   client,
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
}

func mustForgejo(t *testing.T) *forgejo.Client {
	t.Helper()
	c, err := forgejo.New("https://git.host.bzh", "tok")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

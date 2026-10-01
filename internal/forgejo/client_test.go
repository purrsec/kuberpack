package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepoAndBranchSHA(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/flask-uv", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token test-token" {
			t.Errorf("Authorization: %q", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": "pepe/flask-uv",
			"clone_url": "https://git.host.bzh/pepe/flask-uv.git",
			"ssh_url":   "ssh://git@git.host.bzh:30022/pepe/flask-uv.git",
			"private":   true,
			"name":      "flask-uv",
			"owner":     map[string]string{"login": "pepe"},
		})
	})
	mux.HandleFunc("/api/v1/repos/pepe/flask-uv/branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "main",
			"commit": map[string]string{
				"id": "0123456789abcdef0123456789abcdef01234567",
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()

	repo, err := c.Repo(context.Background(), "pepe", "flask-uv")
	if err != nil {
		t.Fatal(err)
	}
	if repo.FullName != "pepe/flask-uv" || repo.CloneURL == "" {
		t.Fatalf("%+v", repo)
	}

	sha, err := c.BranchSHA(context.Background(), "pepe", "flask-uv", "main")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("sha %q", sha)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "bad")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	if _, err := c.Repo(context.Background(), "pepe", "flask-uv"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New("https://git.host.bzh", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseOwnerName(t *testing.T) {
	owner, name, err := ParseOwnerName("pepe/flask-uv")
	if err != nil || owner != "pepe" || name != "flask-uv" {
		t.Fatalf("%s %s %v", owner, name, err)
	}
	if _, _, err := ParseOwnerName("invalid"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDispatchWorkflow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/infra-homelab/actions/workflows/app-release.yaml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		var payload struct {
			Ref    string            `json:"ref"`
			Inputs map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Ref != "main" || payload.Inputs["repository"] != "pepe/hello-world" || payload.Inputs["sha"] == "" {
			t.Fatalf("%+v", payload)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	if err := c.DispatchWorkflow(context.Background(), "pepe", "infra-homelab", "app-release.yaml", "main", map[string]string{
		"repository": "pepe/hello-world",
		"sha":        "abc1234",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRepoOwnerNameFromURL(t *testing.T) {
	owner, name, err := RepoOwnerNameFromURL("https://git.host.bzh/pepe/infra-homelab.git")
	if err != nil || owner != "pepe" || name != "infra-homelab" {
		t.Fatalf("%s %s %v", owner, name, err)
	}
}

func TestRawFileAndResolveCommit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/infra-homelab/raw/kubernetes/vps/apps/site/values.yaml", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "main" {
			t.Errorf("ref %q", r.URL.Query().Get("ref"))
		}
		_, _ = w.Write([]byte("track: main\nimage: \"\"\n"))
	})
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/git/commits/8c06814", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": "8c06814474971005530a724e665ab765b69feb05"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	raw, err := c.RawFile(context.Background(), "pepe", "infra-homelab", "kubernetes/vps/apps/site/values.yaml", "main")
	if err != nil || string(raw) != "track: main\nimage: \"\"\n" {
		t.Fatalf("%q %v", raw, err)
	}
	sha, err := c.ResolveCommit(context.Background(), "pepe", "hello-world", "8c06814")
	if err != nil || sha != "8c06814474971005530a724e665ab765b69feb05" {
		t.Fatalf("%q %v", sha, err)
	}
	full := "0123456789abcdef0123456789abcdef01234567"
	got, err := c.ResolveCommit(context.Background(), "pepe", "hello-world", full)
	if err != nil || got != full {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCreateCommitStatus(t *testing.T) {
	var got struct {
		method string
		path   string
		body   map[string]string
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/statuses/0123456789abcdef0123456789abcdef01234567", func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "status": got.body["state"]})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	if err := c.CreateCommitStatus(context.Background(), "pepe", "hello-world", "0123456789ABCDEF0123456789ABCDEF01234567", CommitStatus{
		State:       StatusPending,
		Description: "Building",
		TargetURL:   "https://hello-world.host.bzh/",
	}); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost {
		t.Fatalf("method %s", got.method)
	}
	if got.body["state"] != StatusPending || got.body["context"] != ProductionContext {
		t.Fatalf("%v", got.body)
	}
	if got.body["target_url"] != "https://hello-world.host.bzh/" {
		t.Fatalf("target_url %q", got.body["target_url"])
	}
}

func TestTruncateStatus(t *testing.T) {
	if TruncateStatus("ok") != "ok" {
		t.Fatal("short")
	}
	long := strings.Repeat("x", 200)
	got := TruncateStatus(long)
	if n := len([]rune(got)); n != 140 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q runes=%d", got, n)
	}
}

func TestCreateHook(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/hooks", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]any{})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "config": map[string]string{"url": "https://kp/hooks/forgejo"}})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	hook, err := c.EnsureHook(context.Background(), "pepe", "hello-world", "https://kp/hooks/forgejo", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if hook.ID != 3 {
		t.Fatalf("%+v", hook)
	}
}

func TestCreateDeployKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/pepe/hello-world/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["read_only"] != true {
			t.Fatalf("%v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 11, "title": body["title"], "read_only": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()
	key, err := c.CreateDeployKey(context.Background(), "pepe", "hello-world", "kuberpack", "ssh-ed25519 AAAA")
	if err != nil || key.ID != 11 {
		t.Fatalf("%+v %v", key, err)
	}
}

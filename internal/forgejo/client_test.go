package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

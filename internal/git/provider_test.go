package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The factory knows the forges, and refuses the ones it does not know.
func TestNewKnowsTheForges(t *testing.T) {
	for _, kind := range Kinds() {
		url := "https://git.example.org"
		switch kind {
		case KindGitHub:
			url = "https://api.github.com"
		case KindGitLab:
			url = "https://gitlab.example.org/api/v4"
		}
		provider, err := New(Config{Kind: kind, URL: url, Token: "tok"})
		if err != nil {
			t.Fatal(err)
		}
		if provider.Name() != kind {
			t.Fatalf("provider: %q", provider.Name())
		}
	}
	if _, err := New(Config{Kind: "bitbucket", URL: "https://bitbucket.org", Token: "tok"}); err == nil {
		t.Fatal("an unknown forge must be refused")
	} else if !strings.Contains(err.Error(), "github") {
		t.Fatalf("the error must list the known forges: %v", err)
	}
}

// The GitHub provider speaks the GitHub API.
func TestGitHubProvider(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("the token must be sent: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if body != nil {
			bodies = append(bodies, body)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/pepe/cornichons":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name": "pepe/cornichons", "clone_url": "https://github.com/pepe/cornichons.git",
				"default_branch": "main", "private": true, "name": "cornichons",
				"owner": map[string]any{"login": "pepe"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/pepe/cornichons/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"sha": "abc123"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/pepe/cornichons/hooks":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/pepe/cornichons/hooks":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/pepe/cornichons/statuses/abc123":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/pepe/cornichons/contents/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content":  "aGVsbG8=",
				"encoding": "base64",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider, err := NewGitHub(server.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	repo, err := provider.Repository(ctx, "pepe", "cornichons")
	if err != nil {
		t.Fatal(err)
	}
	if repo.CloneURL != "https://github.com/pepe/cornichons.git" || repo.DefaultBranch != "main" {
		t.Fatalf("repository: %+v", repo)
	}
	sha, err := provider.BranchSHA(ctx, "pepe", "cornichons", "main")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "abc123" {
		t.Fatalf("sha: %q", sha)
	}
	hook, err := provider.EnsureHook(ctx, "pepe", "cornichons", "https://kp.example/hooks/forgejo", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if hook.ID != 42 {
		t.Fatalf("hook: %+v", hook)
	}
	if err := provider.CommitStatus(ctx, "pepe", "cornichons", "abc123", Status{
		State: StatusSuccess, Description: "Deployed", Context: "kuberpack/production",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := provider.RawFile(ctx, "pepe", "cornichons", "kuberpack.yaml", "main")
	if err != nil {
		t.Fatal(err)
	}
	if raw != "hello" {
		t.Fatalf("raw: %q", raw)
	}

	// The hook carries the secret and the events.
	var hookBody map[string]any
	for _, body := range bodies {
		if body["name"] == "web" {
			hookBody = body
		}
	}
	if hookBody == nil {
		t.Fatalf("the hook was not created: %+v", bodies)
	}
	config, _ := hookBody["config"].(map[string]any)
	if config["secret"] != "s3cret" || config["url"] != "https://kp.example/hooks/forgejo" {
		t.Fatalf("the hook config: %+v", config)
	}
}

// The GitHub webhook is signed with a HMAC of the body.
func TestGitHubWebhookSignature(t *testing.T) {
	provider, err := NewGitHub("https://api.github.com", "tok")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	header := http.Header{}
	header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	header.Set("X-GitHub-Event", "push")

	if !provider.VerifyWebhook("s3cret", header, body) {
		t.Fatal("the signature must be accepted")
	}
	if provider.VerifyWebhook("autre", header, body) {
		t.Fatal("a wrong secret must be refused")
	}
	header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	if provider.VerifyWebhook("s3cret", header, body) {
		t.Fatal("a wrong signature must be refused")
	}
	if provider.Event(header) != "push" {
		t.Fatalf("event: %q", provider.Event(header))
	}
}

// The GitLab provider speaks the GitLab API and checks its own token.
func TestGitLabProvider(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("the token must be sent: %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/pepe/cornichons":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path_with_namespace": "pepe/cornichons", "http_url_to_repo": "https://gitlab.com/pepe/cornichons.git",
				"default_branch": "main", "visibility": "private",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/pepe/cornichons/repository/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"id": "def456"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider, err := NewGitLab(server.URL+"/api/v4", "tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo, err := provider.Repository(ctx, "pepe", "cornichons")
	if err != nil {
		t.Fatal(err)
	}
	if repo.FullName != "pepe/cornichons" || repo.DefaultBranch != "main" {
		t.Fatalf("repository: %+v", repo)
	}
	sha, err := provider.BranchSHA(ctx, "pepe", "cornichons", "main")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "def456" {
		t.Fatalf("sha: %q", sha)
	}
	if provider.WebURL() != server.URL {
		t.Fatalf("web url: %q", provider.WebURL())
	}

	// GitLab sends the secret as is.
	header := http.Header{}
	header.Set("X-Gitlab-Token", "s3cret")
	header.Set("X-Gitlab-Event", "Push Hook")
	if !provider.VerifyWebhook("s3cret", header, nil) {
		t.Fatal("the token must be accepted")
	}
	if provider.VerifyWebhook("autre", header, nil) {
		t.Fatal("a wrong token must be refused")
	}
	if provider.Event(header) != "push" {
		t.Fatalf("event: %q", provider.Event(header))
	}
	header.Set("X-Gitlab-Event", "Merge Request Hook")
	if provider.Event(header) != "pull_request" {
		t.Fatalf("event: %q", provider.Event(header))
	}
}

// The repository of a URL is read from its path, whatever the forge.
func TestRepoOwnerNameFromURL(t *testing.T) {
	cases := map[string][2]string{
		"https://git.host.bzh/pepe/infra-homelab.git": {"pepe", "infra-homelab"},
		"https://github.com/pepe/site":                {"pepe", "site"},
		"https://gitlab.com/group/project.git":        {"group", "project"},
	}
	for url, want := range cases {
		owner, name, err := RepoOwnerNameFromURL(url)
		if err != nil {
			t.Fatal(err)
		}
		if owner != want[0] || name != want[1] {
			t.Fatalf("%s: got %s/%s", url, owner, name)
		}
	}
}

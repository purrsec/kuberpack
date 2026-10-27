package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/netguard"
)

type successfulTestBuilder struct{}

func (successfulTestBuilder) Build(context.Context, builder.Request) error { return nil }

func TestReleaseUsesOnlyTheDedicatedRegistryCredential(t *testing.T) {
	// The release polls its registry until the pushed tag appears: without
	// an allowlisted loopback the environment guard refuses the fixture
	// server and the poll never succeeds.
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	requests := 0
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, token, ok := r.BasicAuth()
		if !ok || user != "images" || token != "registry-only" {
			t.Error("registry did not receive its dedicated credential")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
		w.WriteHeader(http.StatusOK)
	}))
	defer registry.Close()
	previous := checkout
	checkout = func(context.Context, string, string, string, []string) error { return nil }
	defer func() { checkout = previous }()
	provider := fakeProvider{repo: git.Repository{CloneURL: "https://github.com/public/web.git"}}
	result, err := Run(context.Background(), Request{
		Provider: provider, Builder: successfulTestBuilder{}, Token: "gitops-only", RegistryToken: "registry-only",
		RegistryURL: registry.URL, RegistryUser: "images", Owner: "public", Name: "web",
		SHA: strings.Repeat("a", 40), SkipPromote: true, AllowNonHEAD: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests == 0 || !result.SkippedPromote {
		t.Fatal("registry authentication was not exercised")
	}
}

func TestReleaseDoesNotFallBackToTheGitOpsTokenForTheRegistry(t *testing.T) {
	provider := fakeProvider{}
	_, err := Run(context.Background(), Request{Provider: provider, Token: "gitops-only"})
	if err == nil || !strings.Contains(err.Error(), "registry token is required") {
		t.Fatalf("missing registry credential was not refused before the build: %v", err)
	}
}

func TestPinExistingUsesOnlyTheDedicatedRegistryCredential(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "127.0.0.1")
	requests := 0
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, token, ok := r.BasicAuth()
		if !ok || token != "registry-only" {
			t.Error("frozen release forwarded another credential to the registry")
		}
		// Stop before any GitOps promotion: this test exercises credential
		// forwarding over HTTP, without needing a remote forge or cluster.
		w.WriteHeader(http.StatusForbidden)
	}))
	defer registry.Close()
	_, err := PinExisting(context.Background(), Request{
		Provider: fakeProvider{}, Token: "gitops-only", RegistryToken: "registry-only",
		RegistryURL: registry.URL, RegistryUser: "images", Owner: "public", Name: "web", SHA: strings.Repeat("a", 40),
	})
	if err == nil || requests != 1 {
		t.Fatalf("registry flow was not exercised exactly once: requests=%d error=%v", requests, err)
	}
}

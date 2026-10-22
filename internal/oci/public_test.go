package oci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestResolveDigestAgainstARegistry stands in for a registry: it hands
// out a token and answers the manifest with a digest.
func TestResolveDigestAgainstARegistry(t *testing.T) {
	const digest = "sha256:10164ffe04961b02ebc9fa4da12064eeacb8d3bc3e09ceb059f78ce7aaa83207"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/token"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"a-token"}`))
		case strings.Contains(r.URL.Path, "/manifests/"):
			if got := r.Header.Get("Authorization"); got != "Bearer a-token" {
				t.Errorf("the manifest was asked without the token: %q", got)
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	PublicEndpoints["registry.test"] = Endpoints{Auth: server.URL + "/token?scope=repository:", API: server.URL}
	defer delete(PublicEndpoints, "registry.test")

	got, err := ResolveDigest(context.Background(), "registry.test/library/app:1.2.3")
	if err != nil {
		t.Fatalf("the digest was not resolved: %v", err)
	}
	if got != digest {
		t.Errorf("the digest is %q, expected %q", got, digest)
	}
}

// TestResolveDigestLeavesAPinnedReferenceAlone checks that a reference
// that already carries a digest is not touched.
func TestResolveDigestLeavesAPinnedReferenceAlone(t *testing.T) {
	const reference = "docker.io/library/nginx@sha256:10164ffe04961b02ebc9fa4da12064eeacb8d3bc3e09ceb059f78ce7aaa83207"
	got, err := ResolveDigest(context.Background(), reference)
	if err != nil {
		t.Fatalf("a pinned reference was refused: %v", err)
	}
	if got != reference {
		t.Errorf("the reference was changed: %q", got)
	}
}

// TestSplitReference checks how a reference is read.
func TestSplitReference(t *testing.T) {
	cases := []struct {
		reference  string
		host       string
		repository string
		tag        string
	}{
		{"docker.io/library/nginx:1.27", "docker.io", "library/nginx", "1.27"},
		{"library/nginx:1.27", "docker.io", "library/nginx", "1.27"},
		{"nginx", "docker.io", "nginx", ""},
		{"ghcr.io/user345679/kuberpack:1", "ghcr.io", "user345679/kuberpack", "1"},
		{"quay.io/prometheus/node-exporter:v1.8.0", "quay.io", "prometheus/node-exporter", "v1.8.0"},
	}
	for _, c := range cases {
		host, repository, tag, err := splitReference(c.reference)
		if err != nil {
			t.Errorf("%s: %v", c.reference, err)
			continue
		}
		if host != c.host || repository != c.repository || tag != c.tag {
			t.Errorf("%s gives %s/%s:%s", c.reference, host, repository, tag)
		}
	}
}

// TestResolveDigestOnTheInternet resolves a real image. It is skipped
// when the machine has no network.
func TestResolveDigestOnTheInternet(t *testing.T) {
	if os.Getenv("KUBERPACK_TEST_NETWORK") == "" {
		t.Skip("set KUBERPACK_TEST_NETWORK to resolve a real image")
	}
	got, err := ResolveDigest(context.Background(), "docker.io/library/nginx:1.27")
	if err != nil {
		t.Fatalf("the digest of a public image was not resolved: %v", err)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("the digest is %q", got)
	}
}

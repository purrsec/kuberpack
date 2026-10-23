package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract of a service is written from the manifest: the image is empty
// until the control plane pins it, the track follows the branch, and only
// declared secrets are mounted. The service activates after the first pin.
func TestProjectServiceWritesTheContract(t *testing.T) {
	bare := setupAppsGitOps(t)
	platform := Platform{NetworkPolicy: true, IngressTLS: true, ExternalDNSTarget: "95.111.234.93,2a02:c207:2312:1564::1"}
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:        "stateless",
				Repository:  "owner/repo",
				Port:        8080,
				Hostname:    "site.example.org",
				Healthcheck: "/healthz",
			},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: platform,
	}); err != nil {
		t.Fatal(err)
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "hostname: site.example.org") {
		t.Fatalf("values:\n%s", values)
	}
	if !strings.Contains(values, `image: ""`) {
		t.Fatalf("expected empty image:\n%s", values)
	}
	if !strings.Contains(values, "track: main") {
		t.Fatalf("expected track: main:\n%s", values)
	}
	if strings.Contains(values, "command:") {
		t.Fatal("values must not set command")
	}
	if strings.Contains(values, "envFrom:") {
		t.Fatalf("a project must not mount undeclared secrets:\n%s", values)
	}
	if !strings.Contains(values, "2a02:c207:2312:1564::1") {
		t.Fatalf("expected AAAA ExternalDNS target:\n%s", values)
	}
	if strings.Contains(values, "95.111.234.93") {
		t.Fatal("values must not publish an IPv4 ExternalDNS target")
	}
	if !strings.Contains(values, "internet: false") || !strings.Contains(values, "peers: []") {
		t.Fatalf("expected default network allowlist:\n%s", values)
	}

	hr := readFile(t, clone, "kubernetes/vps/projects/demo/site/helmrelease.yaml")
	if !strings.Contains(hr, "name: site") {
		t.Fatalf("helmrelease:\n%s", hr)
	}
	if !strings.Contains(hr, "enable: true") || !strings.Contains(hr, "strategy: rollback") {
		t.Fatalf("HelmRelease must enable tests and rollback:\n%s", hr)
	}

	// Flux must not see the service before an image pin.
	parent := readFile(t, clone, "kubernetes/vps/projects/demo/kustomization.yaml")
	if listedInKustomization([]byte(parent), "site") {
		t.Fatal("Flux must not see the service before an image pin")
	}

	if _, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "kubernetes/vps/projects/demo/site/values.yaml",
		ChartPath:    chartDir(t),
		Image:        mustPin(t),
		Hostname:     "site.example.org",
		Port:         8080,
		Healthcheck:  "/healthz",
		Platform:     platform,
	}); err != nil {
		t.Fatal(err)
	}

	clone = readRemoteTree(t, bare)
	values = readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, mustPin(t).String()) {
		t.Fatalf("image not pinned:\n%s", values)
	}
	parent = readFile(t, clone, "kubernetes/vps/projects/demo/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "site") {
		t.Fatalf("service not enabled in the project parent:\n%s", parent)
	}
}

func TestListedInKustomization(t *testing.T) {
	raw := []byte("resources:\n  - hello-world\n  # - spychain\n")
	if !listedInKustomization(raw, "hello-world") {
		t.Fatal("expected hello-world")
	}
	if listedInKustomization(raw, "hello") {
		t.Fatal("prefix should not match")
	}
	if listedInKustomization(raw, "spychain") {
		t.Fatal("commented resource is not listed")
	}
	out := appendKustomizationResource(raw, "site")
	if !listedInKustomization(out, "site") {
		t.Fatalf("append failed:\n%s", out)
	}
	out = removeKustomizationResource(out, "site")
	if listedInKustomization(out, "site") {
		t.Fatal("remove failed")
	}
}

func setupAppsGitOps(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "gitops.git")
	seed := filepath.Join(dir, "seed")
	gitT(t, "", "init", "--bare", "-b", "main", bare)
	gitT(t, "", "clone", bare, seed)
	path := filepath.Join(seed, "kubernetes", "vps", "apps", "kustomization.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - namespaces.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", ".")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	gitT(t, seed, "push", "-u", "origin", "HEAD:main")
	return bare
}

func readRemoteTree(t *testing.T, bare string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "tree")
	gitT(t, "", "clone", "--branch", "main", bare, clone)
	return clone
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

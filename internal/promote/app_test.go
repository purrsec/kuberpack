package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAppWritesContractAndPromoteEnablesFlux(t *testing.T) {
	bare := setupAppsGitOps(t)
	spec := AppSpec{Name: "site", Hostname: "site.example.org", Port: 8080, Healthcheck: "/healthz"}
	platform := Platform{NetworkPolicy: true, IngressTLS: true, ExternalDNSTarget: "95.111.234.93,2a02:c207:2312:1564::1"}

	reg, err := EnsureApp(context.Background(), EnsureRequest{GitOpsURL: bare, GitOpsBranch: "main", App: spec, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Changed {
		t.Fatal("expected register commit")
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/apps/site/values.yaml")
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
	if !strings.Contains(values, "name: app-site") {
		t.Fatalf("expected envFrom secret app-site:\n%s", values)
	}
	if !strings.Contains(values, "2a02:c207:2312:1564::1") {
		t.Fatalf("expected AAAA ExternalDNS target:\n%s", values)
	}
	if strings.Contains(values, "95.111.234.93") {
		t.Fatal("values must not publish an IPv4 ExternalDNS target")
	}
	if !strings.Contains(readFile(t, clone, "kubernetes/vps/apps/site/helmrelease.yaml"), "name: site") {
		t.Fatal("missing helmrelease")
	}
	hr := readFile(t, clone, "kubernetes/vps/apps/site/helmrelease.yaml")
	if !strings.Contains(hr, "enable: true") || !strings.Contains(hr, "strategy: rollback") {
		t.Fatalf("HelmRelease must enable tests and rollback:\n%s", hr)
	}
	parent := readFile(t, clone, "kubernetes/vps/apps/kustomization.yaml")
	if listedInKustomization([]byte(parent), "site") {
		t.Fatal("Flux must not see the app before an image pin")
	}

	again, err := EnsureApp(context.Background(), EnsureRequest{GitOpsURL: bare, GitOpsBranch: "main", App: spec, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("second register should be a no-op")
	}

	result, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "kubernetes/vps/apps/site/values.yaml",
		ChartPath:    chartDir(t),
		Image:        mustPin(t),
		Hostname:     spec.Hostname,
		Port:         spec.Port,
		Healthcheck:  spec.Healthcheck,
		Platform:     platform,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected promote commit")
	}

	clone = readRemoteTree(t, bare)
	values = readFile(t, clone, "kubernetes/vps/apps/site/values.yaml")
	if !strings.Contains(values, mustPin(t).String()) {
		t.Fatalf("image not pinned:\n%s", values)
	}
	parent = readFile(t, clone, "kubernetes/vps/apps/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "site") {
		t.Fatalf("app not enabled in parent:\n%s", parent)
	}
}

func TestEnsureAppDoesNotOverwriteValues(t *testing.T) {
	bare := setupAppsGitOps(t)
	seed := t.TempDir()
	gitT(t, "", "clone", bare, seed)
	dir := filepath.Join(seed, "kubernetes", "vps", "apps", "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "# keep me\nhostname: site.example.org\nport: 8080\nhealthcheck: /healthz\nimage: old\n"
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", ".")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "custom")
	gitT(t, seed, "push", "origin", "HEAD:main")

	_, err := EnsureApp(context.Background(), EnsureRequest{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		App:          AppSpec{Name: "site", Hostname: "other.example.org", Port: 9090, Healthcheck: "/ready"},
		Platform:     Platform{IngressTLS: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, readRemoteTree(t, bare), "kubernetes/vps/apps/site/values.yaml")
	if !strings.Contains(got, "# keep me") {
		t.Fatalf("overwrote values:\n%s", got)
	}
	if strings.Contains(got, "other.example.org") {
		t.Fatal("replaced hostname")
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

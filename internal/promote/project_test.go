package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureProjectCompilesServicesAddonsPolicies(t *testing.T) {
	bare := setupAppsGitOps(t)
	net := true
	m := ProjectManifest{
		Project:   "mitame",
		Namespace: "mitame",
		Services: map[string]Service{
			"site": {Type: "stateless", Port: 8080, Hostname: "mitame.host.bzh"},
		},
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "umami", Owner: "umami"},
		},
		Policies: Policy{DefaultDeny: true, Internet: &net, Peers: []string{"postgres"}},
	}
	res, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, IngressTLS: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("expected a commit")
	}
	clone := readRemoteTree(t, bare)

	// Human source preserved verbatim.
	manifest := readFile(t, clone, "kubernetes/vps/projects/mitame/kuberpack.yaml")
	if !strings.Contains(manifest, "project: mitame") {
		t.Fatalf("manifest:\n%s", manifest)
	}

	// Service compiled to the stateless contract.
	values := readFile(t, clone, "kubernetes/vps/projects/mitame/site/values.yaml")
	if !strings.Contains(values, "hostname: mitame.host.bzh") {
		t.Fatalf("service values:\n%s", values)
	}
	if !strings.Contains(values, "postgres:mitame") {
		t.Fatalf("expected the addon peer in the service:\n%s", values)
	}
	if !strings.Contains(values, `image: ""`) {
		t.Fatalf("service must start unpinned:\n%s", values)
	}

	// Addon compiled to a CNPG cluster with the expected labels.
	db := readFile(t, clone, "kubernetes/vps/projects/mitame/postgres/database.yaml")
	if !strings.Contains(db, "kind: Cluster") || !strings.Contains(db, "database: umami") {
		t.Fatalf("addon db:\n%s", db)
	}
	if !strings.Contains(db, "kuberpack.io/for: mitame") {
		t.Fatalf("addon labels:\n%s", db)
	}

	// Parent lists both and carries the namespace.
	parent := readFile(t, clone, "kubernetes/vps/projects/mitame/kustomization.yaml")
	if !strings.Contains(parent, "namespace: mitame") {
		t.Fatalf("parent namespace:\n%s", parent)
	}
	if !listedInKustomization([]byte(parent), "site") || !listedInKustomization([]byte(parent), "postgres") {
		t.Fatalf("parent resources:\n%s", parent)
	}
}

func TestEnsureProjectPreservesPinnedImage(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project:  "demo",
		Services: map[string]Service{"app": {Type: "stateless", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Simulate promote pinning an image by editing values on the remote.
	clone := readRemoteTree(t, bare)
	valuesPath := filepath.Join(clone, "kubernetes", "vps", "projects", "demo", "app", "values.yaml")
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), `image: ""`, `image: "reg/app:sha-abc@sha256:dead"`, 1)
	if err := os.WriteFile(valuesPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, clone, "add", "-A")
	gitT(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "pin")
	gitT(t, clone, "push", "origin", "HEAD:main")

	// Recompiling must keep the pinned image.
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	again := readFile(t, readRemoteTree(t, bare), "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(again, "sha256:dead") {
		t.Fatalf("pinned image was overwritten:\n%s", again)
	}
}

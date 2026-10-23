package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectNamespaceOwnsItsNamespace(t *testing.T) {
	manifest := ProjectManifest{Project: "demo", Namespace: "data"}
	if got := ProjectNamespace(manifest, "demo"); got != "data" {
		t.Fatalf("custom namespace = %q", got)
	}
	rendered := RenderProjectNamespace("data", "demo")
	for _, want := range []string{"name: data", "kuberpack.io/project: demo", "app.kubernetes.io/part-of: kuberpack", "pod-security.kubernetes.io/enforce: baseline"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("namespace does not contain %q:\n%s", want, rendered)
		}
	}
}

func TestTenantIsolationLeavesNamespaceToTheProvisioner(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"web": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.example.test"},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, TenantIsolation: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	projectDir := filepath.Join(clone, "kubernetes/vps/projects/demo")
	if _, err := os.Stat(filepath.Join(projectDir, "namespace.yaml")); !os.IsNotExist(err) {
		t.Fatalf("a tenant-isolated project must not carry a Namespace object: %v", err)
	}
	parent := readFile(t, clone, "kubernetes/vps/projects/demo/kustomization.yaml")
	if listedInKustomization([]byte(parent), "namespace.yaml") {
		t.Fatalf("the tenant Kustomization must not apply a Namespace:\n%s", parent)
	}
	if !strings.Contains(parent, "namespace: demo") || !listedInKustomization([]byte(parent), "networkpolicy.yaml") {
		t.Fatalf("the project must still target its namespace and isolate ingress:\n%s", parent)
	}
}

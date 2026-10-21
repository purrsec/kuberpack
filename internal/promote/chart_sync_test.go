package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The chart that is applied must be the one the control plane knows: the
// values it writes are rendered with it.
func TestEnsureChartWritesTheChartInTheRepository(t *testing.T) {
	bare := setupAppsGitOps(t)
	// An old chart in the repository.
	seed := t.TempDir()
	gitT(t, "", "clone", bare, seed)
	old := filepath.Join(seed, "kubernetes", "vps", "charts", "stateless")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "Chart.yaml"), []byte("name: stateless\nversion: 0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", ".")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "old chart")
	gitT(t, seed, "push", "origin", "HEAD:main")

	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "Chart.yaml"), []byte("name: stateless\nversion: 0.1.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "templates", "service.yaml"), []byte("kind: Service\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureChart(context.Background(), ChartRequest{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ChartPath:    local,
		ChartRef:     "./kubernetes/vps/charts/stateless",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("the chart must be written")
	}
	clone := readRemoteTree(t, bare)
	got := readFile(t, clone, "kubernetes/vps/charts/stateless/Chart.yaml")
	if !strings.Contains(got, "0.1.3") {
		t.Fatalf("chart:\n%s", got)
	}
	if !strings.Contains(readFile(t, clone, "kubernetes/vps/charts/stateless/templates/service.yaml"), "kind: Service") {
		t.Fatal("the templates must be written")
	}

	// A second sync changes nothing.
	again, err := EnsureChart(context.Background(), ChartRequest{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ChartPath:    local,
		ChartRef:     "./kubernetes/vps/charts/stateless",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("an unchanged chart must not create a commit")
	}
}

// A chart that lives in a registry is not ours to write.
func TestChartRefInARegistryIsNotWritten(t *testing.T) {
	if got := chartRepoPath("oci://registry.example.org/charts/stateless"); got != "" {
		t.Fatalf("a chart in a registry must not be written: %q", got)
	}
	if got := chartRepoPath("./kubernetes/vps/charts/stateless"); got != "kubernetes/vps/charts/stateless" {
		t.Fatalf("chart path: %q", got)
	}
}

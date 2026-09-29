package promote

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/image"
)

const (
	testSHA    = "0123456789abcdef0123456789abcdef01234567"
	testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestRunPromotesImage(t *testing.T) {
	bare := setupGitOps(t, "hostname: web.example.org\nport: 8080\nhealthcheck: /healthz\nimage: old\n")
	ref := mustPin(t)
	result, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "apps/web/values.yaml",
		ChartPath:    chartDir(t),
		Image:        ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected a GitOps commit")
	}
	if result.App != "web" {
		t.Fatalf("app: got %q", result.App)
	}
	if result.InfraCommitSHA == "" {
		t.Fatal("missing infra commit")
	}

	values := readRemoteValues(t, bare)
	if !strings.Contains(values, ref.String()) {
		t.Fatalf("image not in values:\n%s", values)
	}
	if !strings.Contains(values, "hostname: web.example.org") {
		t.Fatalf("hostname lost:\n%s", values)
	}
	if !strings.Contains(values, "port: 8080") {
		t.Fatalf("port lost:\n%s", values)
	}

	again, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "apps/web/values.yaml",
		ChartPath:    chartDir(t),
		Image:        ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("second promote should be a no-op")
	}
	if again.InfraCommitSHA != result.InfraCommitSHA {
		t.Fatalf("infra SHA changed on no-op: %s vs %s", again.InfraCommitSHA, result.InfraCommitSHA)
	}
}

func TestRunDoesNotPushWhenRenderFails(t *testing.T) {
	bare := setupGitOps(t, "port: 8080\nhealthcheck: /healthz\nimage: old\n")
	head := remoteHead(t, bare)
	_, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "apps/web/values.yaml",
		ChartPath:    chartDir(t),
		Image:        mustPin(t),
	})
	if err == nil {
		t.Fatal("expected helm template to fail without hostname")
	}
	if got := remoteHead(t, bare); got != head {
		t.Fatalf("GitOps moved despite render failure: %s -> %s", head, got)
	}
}

func TestRunRejectsIncompleteRequest(t *testing.T) {
	if _, err := Run(context.Background(), Request{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := Run(context.Background(), Request{
		GitOpsURL:  "x",
		ValuesPath: "../secrets.yaml",
		ChartPath:  "charts/stateless",
		Image:      mustPin(t),
	}); err == nil {
		t.Fatal("expected error for values path escape")
	}
}

func TestRenderChartIncludesRestrictedWorkload(t *testing.T) {
	manifests, err := RenderChart(chartDir(t), "web", filepath.Join(chartDir(t), "ci", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: Deployment",
		"kind: Service",
		"kind: Ingress",
		"kind: NetworkPolicy",
		"helm.sh/hook: test",
		"allowPrivilegeEscalation: false",
		"runAsNonRoot: true",
		"readinessProbe:",
		"host: \"web.example.org\"",
		"web.example.org",
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered manifests missing %q", want)
		}
	}
	if strings.Contains(manifests, "privileged: true") {
		t.Fatal("privileged container rendered")
	}
}

func setupGitOps(t *testing.T, values string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "gitops.git")
	seed := filepath.Join(dir, "seed")
	gitT(t, "", "init", "--bare", "-b", "main", bare)
	gitT(t, "", "clone", bare, seed)
	path := filepath.Join(seed, "apps", "web", "values.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(values), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", ".")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	gitT(t, seed, "push", "-u", "origin", "HEAD:main")
	return bare
}

func readRemoteValues(t *testing.T, bare string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "check")
	gitT(t, "", "clone", "--branch", "main", bare, clone)
	b, err := os.ReadFile(filepath.Join(clone, "apps", "web", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func remoteHead(t *testing.T, bare string) string {
	t.Helper()
	return gitT(t, bare, "rev-parse", "HEAD")
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustPin(t *testing.T) image.Ref {
	t.Helper()
	ref, err := image.Pin("git.host.bzh/pepe/web", testSHA, testDigest)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func chartDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "charts", "stateless"))
}

package promote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A volume with a size is a disk: the chart must create the claim, and the
// pod must mount it. A volume without a size is a temporary directory.
func TestRenderChartCreatesTheClaimOfAVolume(t *testing.T) {
	dir := t.TempDir()
	values := filepath.Join(dir, "values.yaml")
	content := `port: 8080
healthcheck: /
hostname: web.example.org
image: example.org/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
volumes:
  - name: toolbox
    path: /usr/local/bin
    size: 1Gi
  - name: cache
    path: /var/cache
`
	if err := os.WriteFile(values, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := RenderChart(chartDir(t), "web", values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kind: PersistentVolumeClaim") {
		t.Fatalf("the claim of the volume is missing:\n%s", out)
	}
	if !strings.Contains(out, "name: web-toolbox") {
		t.Fatalf("the claim must be named after the release and the volume:\n%s", out)
	}
	if !strings.Contains(out, "storage: \"1Gi\"") {
		t.Fatalf("the size of the volume is missing:\n%s", out)
	}
	if !strings.Contains(out, "claimName: web-toolbox") {
		t.Fatalf("the pod must mount the claim:\n%s", out)
	}
	// A volume without a size stays a temporary directory.
	if strings.Contains(out, "name: web-cache") {
		t.Fatalf("a volume without a size must not have a claim:\n%s", out)
	}
}

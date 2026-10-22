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
	// The chart does not create the claim: the platform renders it, in the
	// manifest of the application. The chart only mounts it.
	if !strings.Contains(out, "claimName: web-toolbox") {
		t.Fatalf("the pod must mount the claim of the volume:\n%s", out)
	}
	// A volume without a size stays a temporary directory.
	if strings.Contains(out, "claimName: web-cache") {
		t.Fatalf("a volume without a size must not have a claim:\n%s", out)
	}
	if strings.Contains(out, "kind: PersistentVolumeClaim") {
		t.Fatalf("the chart must not create the claim of a volume:\n%s", out)
	}
}

// The claim of a volume carries the name of the application: two
// applications of the same namespace do not share their disks.
func TestRenderVolumeClaimCarriesTheNameOfTheApplication(t *testing.T) {
	spec := AppSpec{Name: "stirling", Namespace: "stirling"}
	out := RenderVolumeClaim(Volume{Name: "toolbox", Path: "/usr/local/bin", Size: "1Gi"}, spec, Platform{})
	if !strings.Contains(out, "name: stirling-toolbox") {
		t.Fatalf("the claim must carry the name of the application:\n%s", out)
	}
	if !strings.Contains(out, "namespace: stirling") {
		t.Fatalf("the claim must be in the namespace of the application:\n%s", out)
	}
	if !strings.Contains(out, "storage: 1Gi") {
		t.Fatalf("the size of the volume is missing:\n%s", out)
	}
}

// The pods of the application carry the labels of the platform: the
// NetworkPolicy of an addon selects the applications that may reach it.
func TestRenderChartGivesTheLabelsToThePods(t *testing.T) {
	dir := t.TempDir()
	values := filepath.Join(dir, "values.yaml")
	content := `port: 8080
healthcheck: /
hostname: web.example.org
image: example.org/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`
	if err := os.WriteFile(values, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := RenderChart(chartDir(t), "web", values)
	if err != nil {
		t.Fatal(err)
	}
	// The labels of the pod template, not only the ones of the Deployment.
	marker := "template:"
	index := strings.Index(out, marker)
	if index < 0 {
		t.Fatalf("the pod template is missing:\n%s", out)
	}
	template := out[index:]
	if !strings.Contains(template, "app.kubernetes.io/part-of: kuberpack") {
		t.Fatalf("the pods must carry the labels of the platform:\n%s", template)
	}
}

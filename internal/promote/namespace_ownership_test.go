package promote

import (
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

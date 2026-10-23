package charts

import (
	"errors"
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestTenantIsolationRendersBoundedRBACAndAdmission(t *testing.T) {
	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ToRenderValues(chart, map[string]any{"tenantIsolation": true}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "kuberpack", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, values)
	if err != nil {
		t.Fatal(err)
	}
	rbac := files["kuberpack/templates/rbac.yaml"]
	if !strings.Contains(rbac, "name: kuberpack-tenant") {
		t.Fatal("the tenant ClusterRole is missing")
	}
	if !strings.Contains(rbac, "kuberpack-provisioner") {
		t.Fatal("the provisioner ClusterRole is missing")
	}
	admission := files["kuberpack/templates/tenant-admission.yaml"]
	for _, name := range []string{
		"kuberpack-tenant-namespaces",
		"kuberpack-tenant-serviceaccounts",
		"kuberpack-tenant-rolebindings",
		"kuberpack-tenant-kustomizations",
		"kuberpack-control-rolebindings",
	} {
		if !strings.Contains(admission, name) {
			t.Fatalf("missing admission policy %s", name)
		}
	}
	// Every policy has a binding: the policy is inert without it.
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		count := 0
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(admission), 4096)
		for {
			var object unstructured.Unstructured
			if err := decoder.Decode(&object); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatal(err)
			}
			if object.GetKind() == kind {
				count++
			}
		}
		if count != 5 {
			t.Fatalf("%s count = %d, want 5", kind, count)
		}
	}
}

func TestTenantIsolationIsOffByDefault(t *testing.T) {
	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ToRenderValues(chart, map[string]any{}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "kuberpack", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, values)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files["kuberpack/templates/rbac.yaml"], "kuberpack-tenant") {
		t.Fatal("tenant RBAC must be opt-in")
	}
}

func TestReloaderIsOptInAndHasItsOwnIdentity(t *testing.T) {
	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	render := func(values map[string]any) string {
		v, err := chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "kuberpack", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
		if err != nil {
			t.Fatal(err)
		}
		files, err := engine.Render(chart, v)
		if err != nil {
			t.Fatal(err)
		}
		return files["kuberpack/templates/reloader.yaml"]
	}
	if strings.TrimSpace(render(map[string]any{})) != "" {
		t.Fatal("the reloader must be opt-in")
	}
	body := render(map[string]any{"reloader": map[string]any{"enabled": true}})
	for _, want := range []string{"name: kuberpack-reloader", "name: kuberpack-reloader-tenant", "kind: ClusterRole", "kind: Deployment"} {
		if !strings.Contains(body, want) {
			t.Fatalf("reloader manifest does not contain %q", want)
		}
	}
}

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

func TestNodeMetricsPermissionCannotProxyOrExecuteCommands(t *testing.T) {
	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		values, err := chartutil.ToRenderValues(chart, map[string]any{"nodeMetrics": map[string]any{"enabled": enabled}}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "custom-control", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
		if err != nil {
			t.Fatal(err)
		}
		files, err := engine.Render(chart, values)
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(files["kuberpack/templates/rbac.yaml"]), 4096)
		clusterRoles, clusterBindings, buildRoles := 0, 0, 0
		for {
			var object unstructured.Unstructured
			if err := decoder.Decode(&object); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatal(err)
			}
			switch object.GetKind() {
			case "ClusterRole":
				clusterRoles++
				if object.GetName() != "custom-control-kuberpack-node-metrics" {
					t.Fatal("unscoped metrics role name")
				}
				rules, _, _ := unstructured.NestedSlice(object.Object, "rules")
				if len(rules) != 1 {
					t.Fatal("unexpected cluster permissions")
				}
				rule := rules[0].(map[string]any)
				verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				if len(verbs) != 1 || verbs[0] != "get" || len(resources) != 2 || resources[0] != "nodes" || resources[1] != "nodes/metrics" {
					t.Fatalf("overbroad node metrics permissions: %+v", rule)
				}
			case "ClusterRoleBinding":
				clusterBindings++
				subjects, _, _ := unstructured.NestedSlice(object.Object, "subjects")
				if len(subjects) != 1 || subjects[0].(map[string]any)["namespace"] != "custom-control" {
					t.Fatal("wrong control-plane subject")
				}
			case "Role":
				if object.GetName() == "kuberpack-build-dispatch" {
					buildRoles++
					if object.GetNamespace() != "kuberpack-build" || object.Object["rules"] == nil {
						t.Fatal("builder dispatch role was damaged")
					}
				}
			}
		}
		want := 0
		if enabled {
			want = 1
		}
		if clusterRoles != want || clusterBindings != want || buildRoles != 1 {
			t.Fatalf("unexpected RBAC objects: roles=%d bindings=%d build=%d", clusterRoles, clusterBindings, buildRoles)
		}
	}
}

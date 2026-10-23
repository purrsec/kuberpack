package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeYAML "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestInfisicalBootstrapKeepsCredentialsWhileWebLosesThem(t *testing.T) {
	web := &chart.File{Name: "templates/infisical.yaml", Data: []byte("apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      serviceAccountName: infisical-infisical\n")}
	bootstrap := &chart.File{Name: "templates/bootstrap-job.yaml", Data: []byte("kind: Job\nspec:\n  template:\n    spec:\n      serviceAccountName: infisical-infisical\n")}
	before := string(bootstrap.Data)
	loaded := &chart.Chart{Templates: []*chart.File{web, bootstrap}}
	if err := hardenInfisicalRuntimeChart(loaded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(web.Data), "automountServiceAccountToken: false") {
		t.Fatal("web process still gets an API token")
	}
	if string(bootstrap.Data) != before {
		t.Fatal("bootstrap Job was modified")
	}
	if err := hardenInfisicalRuntimeChart(&chart.Chart{}); err == nil {
		t.Fatal("missing upstream deployment was ignored")
	}
}

func TestInfisicalFluxPostRendererTargetsOnlyRuntimeDeployment(t *testing.T) {
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/chart"); err != nil {
		t.Fatal(err)
	}
	objects := ""
	for _, item := range []struct{ Kind, Name string }{{"Deployment", infisicalRuntimeDeployment}, {"Deployment", "unrelated"}, {"Job", "infisical-bootstrap-1"}} {
		group := "apps/v1"
		if item.Kind == "Job" {
			group = "batch/v1"
		}
		objects += "apiVersion: " + group + "\nkind: " + item.Kind + "\nmetadata:\n  name: " + item.Name + "\n  namespace: infisical\nspec:\n  template:\n    spec:\n      automountServiceAccountToken: true\n      containers:\n        - name: test\n          image: example.org/image:1\n---\n"
	}
	patches := infisicalRuntimePostRenderers()[0].(map[string]any)["kustomize"].(map[string]any)["patches"]
	parent, err := yaml.Marshal(map[string]any{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": []string{"objects.yaml"}, "patches": patches})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/chart/kustomization.yaml", parent); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/chart/objects.yaml", []byte(objects)); err != nil {
		t.Fatal(err)
	}
	result, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(fs, "/chart")
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range result.Resources() {
		value, err := object.GetFieldValue("spec.template.spec.automountServiceAccountToken")
		if err != nil {
			t.Fatal(err)
		}
		want := object.GetName() != infisicalRuntimeDeployment
		if value != want {
			t.Fatalf("%s %s automount=%v want %v", object.GetKind(), object.GetName(), value, want)
		}
	}
}

func TestInfisicalDependenciesRestrictDatabaseNetworkAndTokens(t *testing.T) {
	for _, service := range infisicalServices("infisical.example.org") {
		if service.Name != "infisical" {
			continue
		}
		pg := service.Values["postgresql"].(map[string]any)["primary"].(map[string]any)
		redis := service.Values["redis"].(map[string]any)
		for name, policy := range map[string]map[string]any{"postgres": pg["networkPolicy"].(map[string]any), "redis": redis["networkPolicy"].(map[string]any)} {
			if policy["allowExternal"] != false || policy["allowExternalEgress"] != false {
				t.Fatalf("%s allows unrestricted network", name)
			}
			if policy["ingressNSMatchLabels"].(map[string]any)["kubernetes.io/metadata.name"] != infisicalNamespace {
				t.Fatalf("%s accepts another namespace", name)
			}
			if policy["ingressNSPodMatchLabels"].(map[string]any)["component"] != "infisical" {
				t.Fatalf("%s accepts arbitrary pods", name)
			}
		}
		if pg["automountServiceAccountToken"] != false || redis["master"].(map[string]any)["automountServiceAccountToken"] != false || redis["replica"].(map[string]any)["automountServiceAccountToken"] != false {
			t.Fatal("database pods may mount API credentials")
		}
		return
	}
	t.Fatal("Infisical chart missing")
}

// This checks the actual pinned upstream templates, not a locally mirrored
// chart. Opt in with the same network flag as the existing dependency tests.
func TestInfisicalChartsEnforceScopeAndRuntimeHardening(t *testing.T) {
	if os.Getenv("KUBERPACK_TEST_NETWORK") == "" {
		t.Skip("set KUBERPACK_TEST_NETWORK to fetch the charts")
	}
	settings := cli.New()
	for _, service := range infisicalServices("infisical.example.org") {
		loaded, err := fetchInfisicalChart(settings, service)
		if err != nil {
			t.Fatal(err)
		}
		if service.Name == "infisical-operator" {
			service.Values["scopedNamespaces"] = []string{"infisical", "demo-runtime"}
		}
		values, err := chartutil.ToRenderValues(loaded, service.Values, chartutil.ReleaseOptions{Name: service.Name, Namespace: infisicalNamespace, Revision: 1, IsInstall: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := engine.Render(loaded, values)
		if err != nil {
			t.Fatal(err)
		}
		var runtime, bootstrap, dbs, scopeRoles int
		for template, raw := range rendered {
			if strings.HasSuffix(template, "NOTES.txt") {
				continue
			}
			decoder := kubeYAML.NewYAMLOrJSONDecoder(strings.NewReader(raw), 4096)
			for {
				var object unstructured.Unstructured
				err := decoder.Decode(&object)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if object.GetKind() == "" {
					continue
				}
				kind, name := object.GetKind(), object.GetName()
				if kind == "Deployment" && name == infisicalRuntimeDeployment {
					runtime++
					value, found, _ := unstructured.NestedBool(object.Object, "spec", "template", "spec", "automountServiceAccountToken")
					if !found || value {
						t.Fatal("Infisical runtime mounts Kubernetes credentials")
					}
				}
				if kind == "Job" && strings.Contains(name, "bootstrap") {
					bootstrap++
					value, found, _ := unstructured.NestedBool(object.Object, "spec", "template", "spec", "automountServiceAccountToken")
					if found && !value {
						t.Fatal("bootstrap cannot publish credentials")
					}
				}
				if kind == "StatefulSet" {
					dbs++
					value, found, _ := unstructured.NestedBool(object.Object, "spec", "template", "spec", "automountServiceAccountToken")
					if !found || value {
						t.Fatalf("%s mounts Kubernetes credentials", name)
					}
				}
				if strings.HasSuffix(name, "-manager-role") {
					if kind == "ClusterRole" {
						t.Fatal("operator has cluster-wide manager privileges")
					}
					if kind == "Role" {
						scopeRoles++
						if object.GetNamespace() != "infisical" && object.GetNamespace() != "demo-runtime" {
							t.Fatal("operator role escaped namespace scope")
						}
					}
				}
			}
		}
		if service.Name == "infisical" && (runtime != 1 || bootstrap != 1 || dbs < 2) {
			t.Fatalf("runtime=%d bootstrap=%d databases=%d", runtime, bootstrap, dbs)
		}
		if service.Name == "infisical-operator" && scopeRoles != 2 {
			t.Fatalf("scoped roles=%d", scopeRoles)
		}
	}
}

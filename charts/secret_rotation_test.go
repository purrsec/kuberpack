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
	kubeYAML "k8s.io/apimachinery/pkg/util/yaml"
)

const rotationTestImage = "example.org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// renderStateless rend le chart d'application avec des valeurs de test.
func renderStateless(t *testing.T, values map[string]any) map[string]string {
	t.Helper()
	chart, err := loader.Load("stateless")
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"image":       rotationTestImage,
		"healthcheck": "/healthz",
		"hostname":    "web.example.org",
	}
	for key, value := range values {
		base[key] = value
	}
	renderValues, err := chartutil.ToRenderValues(chart, base, chartutil.ReleaseOptions{Name: "web", Namespace: "demo", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// decodeObjects décode tous les objets d'un template rendu.
func decodeObjects(t *testing.T, raw string) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	decoder := kubeYAML.NewYAMLOrJSONDecoder(strings.NewReader(raw), 4096)
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if object.GetKind() != "" {
			objects = append(objects, &object)
		}
	}
	return objects
}

func renderDeployment(t *testing.T, values map[string]any) *unstructured.Unstructured {
	t.Helper()
	for _, object := range decodeObjects(t, renderStateless(t, values)["stateless/templates/deployment.yaml"]) {
		if object.GetKind() == "Deployment" {
			return object
		}
	}
	t.Fatal("the chart did not render a Deployment")
	return nil
}

// Le chart pose l'annotation sur les métadonnées du Deployment, jamais sur le
// template de pod : l'operateur Infisical lit l'annotation du workload et
// écrit lui-même l'etag dans le template pour déclencher le rollout. Sans
// annotation, aucun redémarrage : la valeur par défaut est vide.
func TestStatelessChartRendersDeploymentAnnotationsAtWorkloadLevel(t *testing.T) {
	plain := renderDeployment(t, nil)
	if annotations, found, err := unstructured.NestedStringMap(plain.Object, "metadata", "annotations"); err != nil || found {
		t.Fatalf("le chart ne doit rendre aucune annotation par défaut : %v %v", annotations, err)
	}

	reload := renderDeployment(t, map[string]any{
		"deploymentAnnotations": map[string]any{"secrets.infisical.com/auto-reload": "true"},
	})
	annotations, found, err := unstructured.NestedStringMap(reload.Object, "metadata", "annotations")
	if err != nil || !found {
		t.Fatalf("annotations du Deployment : %v %v", annotations, err)
	}
	if annotations["secrets.infisical.com/auto-reload"] != "true" {
		t.Fatalf("annotation de rotation absente : %+v", annotations)
	}
	templateAnnotations, found, err := unstructured.NestedStringMap(reload.Object, "spec", "template", "metadata", "annotations")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		if _, present := templateAnnotations["secrets.infisical.com/auto-reload"]; present {
			t.Fatal("l'annotation de rotation doit rester sur le Deployment : l'operateur écrit le template lui-même")
		}
	}
}

// Le chart d'application ne crée aucun objet RBAC : la rotation repose sur les
// droits que l'operateur Infisical possède déjà dans son scope. Le chart de la
// plateforme ne gagne pas non plus l'accès aux Secrets applicatifs.
func TestSecretRotationGrantsNoSecretReadPermission(t *testing.T) {
	for template, raw := range renderStateless(t, map[string]any{
		"deploymentAnnotations": map[string]any{"secrets.infisical.com/auto-reload": "true"},
	}) {
		if strings.HasSuffix(template, "NOTES.txt") {
			continue
		}
		for _, object := range decodeObjects(t, raw) {
			switch object.GetKind() {
			case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
				t.Fatalf("le chart d'application ne doit créer aucun RBAC (%s/%s)", object.GetKind(), object.GetName())
			}
		}
	}

	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	renderValues, err := chartutil.ToRenderValues(chart, map[string]any{}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "control", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.36.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	for template, raw := range files {
		if strings.HasSuffix(template, "NOTES.txt") {
			continue
		}
		for _, object := range decodeObjects(t, raw) {
			switch object.GetKind() {
			case "Role", "ClusterRole":
			default:
				continue
			}
			rules, _, _ := unstructured.NestedSlice(object.Object, "rules")
			for _, rawRule := range rules {
				rule, _ := rawRule.(map[string]any)
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
				reads := false
				for _, verb := range verbs {
					if verb == "get" || verb == "list" || verb == "watch" || verb == "*" {
						reads = true
					}
				}
				for _, resource := range resources {
					if resource == "secrets" || strings.HasPrefix(resource, "secrets/") {
						if reads {
							t.Fatalf("le control plane ne doit pas pouvoir lire les Secrets (%s/%s)", object.GetKind(), object.GetName())
						}
					}
				}
			}
		}
	}
}

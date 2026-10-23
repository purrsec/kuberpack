package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestInstallerSeedsScopeBeforeHelmAndPreservesFluxMetadata(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	i := &installation{work: work, c: setup.Config{Root: "clusters/test", Namespace: "control", Infisical: true}, kube: fake.NewSimpleClientset()}
	p := i.infisicalScopePlatform()
	dir := filepath.Join(work, filepath.FromSlash(p.ProjectDir("demo")))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kuberpack.yaml"), []byte("project: demo\nnamespace: demo-runtime\nservices:\n  app:\n    image: example.org/app:1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	values, err := i.seedInfisicalOperatorScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if values["scopedRBAC"] != true || !reflect.DeepEqual(values["scopedNamespaces"], []any{"demo-runtime", "infisical"}) {
		t.Fatalf("values=%v", values)
	}
	if _, err := i.kube.CoreV1().Namespaces().Get(ctx, "demo-runtime", metav1.GetOptions{}); err != nil {
		t.Fatal("target namespace must predate the operator's Role")
	}
	cm, err := i.kube.CoreV1().ConfigMaps("control").Get(ctx, promote.InfisicalOperatorScopeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Labels["kustomize.toolkit.fluxcd.io/name"] = "kuberpack-projects"
	if _, err := i.kube.CoreV1().ConfigMaps("control").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	client := i.kube.(*fake.Clientset)
	client.ClearActions()
	if _, err := i.seedInfisicalOperatorScope(ctx); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" || action.GetVerb() == "create" {
			t.Fatalf("noop reinstall mutated %s", action.GetResource().Resource)
		}
	}
}

func TestDefaultInfisicalOperatorValuesAreScoped(t *testing.T) {
	for _, service := range infisicalServices("infisical.example.org") {
		if service.Name == "infisical-operator" {
			if service.Values["scopedRBAC"] != true || !reflect.DeepEqual(service.Values["scopedNamespaces"], []string{infisicalNamespace}) {
				t.Fatalf("operator values=%v", service.Values)
			}
			return
		}
	}
	t.Fatal("operator missing")
}

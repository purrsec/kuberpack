package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func tenantTestServer() *Server {
	return New(Config{
		TenantIsolation: true,
		AppsSource:      "kuberpack-apps",
		Kube:            fake.NewSimpleClientset(),
		KubeDynamic:     dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		Platform: promote.Platform{
			ControlPlaneNamespace: "kuberpack",
			ProjectsDir:           "projects",
		},
	})
}

func TestProjectTenantIsProvisionedAndDeleted(t *testing.T) {
	ctx := context.Background()
	s := tenantTestServer()

	if err := s.ensureProjectTenant(ctx, "demo", "demo"); err != nil {
		t.Fatal(err)
	}

	namespace, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if namespace.Labels["kuberpack.io/project"] != "demo" || namespace.Labels["app.kubernetes.io/part-of"] != "kuberpack" {
		t.Fatalf("namespace labels: %#v", namespace.Labels)
	}

	serviceAccount, err := s.cfg.Kube.CoreV1().ServiceAccounts("kuberpack").Get(ctx, "kuberpack-tenant-demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if serviceAccount.AutomountServiceAccountToken == nil || *serviceAccount.AutomountServiceAccountToken {
		t.Fatal("the tenant identity must not mount a token")
	}

	binding, err := s.cfg.Kube.RbacV1().RoleBindings("demo").Get(ctx, "kuberpack-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.RoleRef.Name != "kuberpack-tenant" || len(binding.Subjects) != 1 || binding.Subjects[0].Name != "kuberpack-tenant-demo" {
		t.Fatalf("role binding: %#v", binding)
	}

	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	kustomization, err := s.cfg.KubeDynamic.Resource(gvr).Namespace("kuberpack").Get(ctx, "kuberpack-project-demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := kustomization.Object["spec"].(map[string]any)
	if spec["serviceAccountName"] != "kuberpack-tenant-demo" || spec["targetNamespace"] != "demo" || spec["path"] != "./projects/demo" {
		t.Fatalf("kustomization spec: %#v", spec)
	}

	if err := s.deleteProjectTenant(ctx, "demo", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cfg.KubeDynamic.Resource(gvr).Namespace("kuberpack").Get(ctx, "kuberpack-project-demo", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the project Kustomization must be removed: %v", err)
	}
	if _, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, "demo", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the owned namespace must be removed: %v", err)
	}
}

func TestProjectTenantLeavesAnUnlabelledNamespace(t *testing.T) {
	ctx := context.Background()
	s := tenantTestServer()
	if err := s.ensureNamespace(ctx, "foreign", map[string]string{"kuberpack.io/project": "demo"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a namespace that does not belong to this project.
	namespace, _ := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, "foreign", metav1.GetOptions{})
	namespace.Labels["kuberpack.io/project"] = "other"
	if _, err := s.cfg.Kube.CoreV1().Namespaces().Update(ctx, namespace, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteProjectTenant(ctx, "demo", "foreign"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, "foreign", metav1.GetOptions{}); err != nil {
		t.Fatalf("an unowned namespace must be preserved: %v", err)
	}
}

func TestTenantScopeIncludesPublishedApplications(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SyncImageApps(ctx, "tenant-demo", []store.App{{Name: "tenant-demo-web", Project: "tenant-demo", Namespace: "tenant-demo"}}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		TenantIsolation: true,
		Store:           db,
		Kube:            fake.NewSimpleClientset(),
		Platform: promote.Platform{
			ControlPlaneNamespace:  "kuberpack",
			InfisicalNamespace:     "infisical",
			InfisicalOperatorScope: true,
		},
	})
	if err := s.syncProjectTenantScope(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := s.cfg.Kube.CoreV1().ConfigMaps("kuberpack").Get(ctx, promote.InfisicalOperatorScopeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	values := current.Data["values.yaml"]
	if !strings.Contains(values, "tenant-demo") || !strings.Contains(values, "infisical") {
		t.Fatalf("scope values:\n%s", values)
	}
}

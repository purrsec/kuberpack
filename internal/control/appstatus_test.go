package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func statusDeployment() *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "custom", UID: "deployment", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry/app@sha256:current"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}}
}

func statusRelease() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "web", "namespace": "custom", "generation": int64(2)}, "spec": map[string]any{}, "status": map[string]any{"observedGeneration": int64(2), "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2), "reason": "UpgradeSucceeded", "message": "Release is ready"}}}}}
}

func TestDeploymentStatusUsesCurrentReplicasAndCurrentReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		change      func(*appsv1.Deployment, *unstructured.Unstructured)
	}{
		{"healthy", "running", func(*appsv1.Deployment, *unstructured.Unstructured) {}},
		{"old deployment generation", "deploying", func(d *appsv1.Deployment, _ *unstructured.Unstructured) { d.Status.ObservedGeneration = 1 }},
		{"old release condition", "deploying", func(_ *appsv1.Deployment, r *unstructured.Unstructured) { r.SetGeneration(3) }},
		{"rolling with old replicas still serving", "deploying", func(d *appsv1.Deployment, _ *unstructured.Unstructured) {
			d.Status.Replicas = 2
			d.Status.UpdatedReplicas = 0
		}},
		{"ready but minimum availability not reached", "deploying", func(d *appsv1.Deployment, _ *unstructured.Unstructured) { d.Status.AvailableReplicas = 0 }},
		{"scaled to zero", "stopped", func(d *appsv1.Deployment, _ *unstructured.Unstructured) {
			*d.Spec.Replicas = 0
			d.Status = appsv1.DeploymentStatus{ObservedGeneration: 2}
		}},
		{"still stopping", "deploying", func(d *appsv1.Deployment, _ *unstructured.Unstructured) { *d.Spec.Replicas = 0 }},
		{"deadline exceeded", "failed", func(d *appsv1.Deployment, _ *unstructured.Unstructured) {
			d.Status.ReadyReplicas = 0
			d.Status.AvailableReplicas = 0
			d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
		}},
		{"failed upgrade with old replicas serving", "degraded", func(_ *appsv1.Deployment, r *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(r.Object, []any{map[string]any{"type": "Ready", "status": "False", "observedGeneration": int64(2), "reason": "UpgradeFailed", "message": "Upgrade failed"}}, "status", "conditions")
		}},
		{"suspended reconciliation does not stop serving", "running", func(_ *appsv1.Deployment, r *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(r.Object, true, "spec", "suspend")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r := statusDeployment(), statusRelease()
			tc.change(d, r)
			s := New(Config{Kube: fake.NewSimpleClientset(d), KubeDynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), r)})
			got := s.observeApp(context.Background(), store.App{Name: "demo-web", Project: "demo", Namespace: "custom"})
			if got.State != tc.state {
				t.Fatalf("got %+v; want %s", got, tc.state)
			}
		})
	}
}

func TestStatusDistinguishesMissingDeploymentAndUnreadableCluster(t *testing.T) {
	app := store.App{Name: "demo-web", Project: "demo", Namespace: "custom"}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s := New(Config{Kube: fake.NewSimpleClientset(), KubeDynamic: dyn})
	if got := s.observeApp(context.Background(), app); got.State != "pending" || got.Replicas != nil {
		t.Fatalf("missing deployment: %+v", got)
	}
	s.cfg.Kube = nil
	if got := s.observeApp(context.Background(), app); got.State != "unknown" || got.Replicas != nil {
		t.Fatalf("missing cluster: %+v", got)
	}
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", nil)
	})
	s.cfg.Kube = client
	if got := s.observeApp(context.Background(), app); got.State != "unknown" || !strings.Contains(got.Message, "Forbidden") {
		t.Fatalf("forbidden observation: %+v", got)
	}
}

func TestPodFailureUsesOwnedCurrentRevisionAndDoesNotExposeTerminationContents(t *testing.T) {
	d := statusDeployment()
	d.Status.ReadyReplicas = 0
	d.Status.AvailableReplicas = 0
	controller := true
	makeSet := func(name, revision string, uid types.UID) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "custom", UID: uid, Labels: map[string]string{"app": "web"}, Annotations: map[string]string{"deployment.kubernetes.io/revision": revision}, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: d.UID, Controller: &controller}}}}
	}
	makePod := func(name string, uid types.UID) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "custom", Labels: map[string]string{"app": "web"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: uid, Controller: &controller}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "sensitive details"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "a secret must not appear"}}}}}}
	}
	old, current := makeSet("old", "1", "old"), makeSet("current", "2", "current")
	client := fake.NewSimpleClientset(d, old, current, makePod("old-pod", old.UID))
	s := New(Config{Kube: client, KubeDynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), statusRelease())})
	app := store.App{Name: "demo-web", Project: "demo", Namespace: "custom"}
	if got := s.observeApp(context.Background(), app); got.State != "deploying" {
		t.Fatalf("old crashed pod changed current rollout status: %+v", got)
	}
	_, err := client.CoreV1().Pods("custom").Create(context.Background(), makePod("current-pod", current.UID), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := s.observeApp(context.Background(), app)
	if got.State != "failed" || got.Reason != "CrashLoopBackOff" || strings.Contains(got.Message, "sensitive") || strings.Contains(got.Message, "secret") {
		t.Fatalf("current failure: %+v", got)
	}
}

func TestStatusAPIIsAuthenticatedAndPreservesCustomNamespaceForPublishedServices(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := fake.NewSimpleClientset(statusDeployment())
	s := New(Config{Store: db, APIToken: "api", Kube: client, KubeDynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), statusRelease())})
	if err := s.reconcileApps(context.Background(), "demo", []promote.ProjectApp{{Name: "demo-web", Namespace: "custom", Image: "registry/app@sha256:current"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, token string
		code        int
	}{{"/api/v1/apps/demo-web/status", "", 401}, {"/api/v1/apps/unknown/status", "api", 404}, {"/api/v1/apps/demo-web/status", "api", 200}, {"/api/v1/apps/demo-web", "api", 200}, {"/api/v1/apps", "api", 200}} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if tc.code == 200 {
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if tc.path == "/api/v1/apps" {
				result = result["apps"].([]any)[0].(map[string]any)
			}
			status := result["deployment"].(map[string]any)
			if status["state"] != "running" || status["namespace"] != "custom" {
				t.Fatalf("unexpected observed status: %+v", status)
			}
		}
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("deployment observation read secrets")
		}
	}
}

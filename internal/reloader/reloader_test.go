package reloader

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func fixture(version string, autoReload bool) (*fake.Clientset, *appsv1.Deployment, *corev1.Secret) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo", Labels: map[string]string{"kuberpack.io/project": "demo"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-web", Namespace: "demo", Annotations: map[string]string{SecretVersionAnnotation: version}}}
	annotations := map[string]string{}
	if autoReload {
		annotations[AutoReloadAnnotation] = "true"
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", Annotations: annotations},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Env:  []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-web"}, Key: "PASSWORD"}}}},
		}}}}},
	}
	return fake.NewSimpleClientset(namespace, secret, deployment), deployment, secret
}

func TestReconcileRestartsOnlyWhenTheSecretVersionChanges(t *testing.T) {
	ctx := context.Background()
	client, _, secret := fixture("v1", true)
	changed, err := Reconcile(ctx, client)
	if err != nil || changed != 1 {
		t.Fatalf("first reconcile: changed=%d err=%v", changed, err)
	}
	deployment, _ := client.AppsV1().Deployments("demo").Get(ctx, "web", metav1.GetOptions{})
	if got := deployment.Spec.Template.Annotations[RevisionAnnotation]; got != "app-web=v1" {
		t.Fatalf("revision = %q", got)
	}
	if changed, err = Reconcile(ctx, client); err != nil || changed != 0 {
		t.Fatalf("stable reconcile: changed=%d err=%v", changed, err)
	}
	secret.Annotations[SecretVersionAnnotation] = "v2"
	if _, err := client.CoreV1().Secrets("demo").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if changed, err = Reconcile(ctx, client); err != nil || changed != 1 {
		t.Fatalf("rotated reconcile: changed=%d err=%v", changed, err)
	}
	deployment, _ = client.AppsV1().Deployments("demo").Get(ctx, "web", metav1.GetOptions{})
	if got := deployment.Spec.Template.Annotations[RevisionAnnotation]; got != "app-web=v2" {
		t.Fatalf("revision after rotation = %q", got)
	}
}

func TestReconcileIgnoresWorkloadsThatDidNotOptIn(t *testing.T) {
	client, _, _ := fixture("v1", false)
	if changed, err := Reconcile(context.Background(), client); err != nil || changed != 0 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
}

package control

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"git.host.bzh/pepe/kuberpack/internal/store"
)

// The values of an application are written in the cluster: the release
// reads them, and the platform does not wait for a controller to write
// them.
func TestSyncAppValuesWritesTheValuesInTheCluster(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := New(Config{
		Kube: client,
		GitOpsFile: func(context.Context, store.App) ([]byte, error) {
			return []byte("replicas: 2\n"), nil
		},
	})
	app := store.App{Name: "comptoir"}
	if err := s.syncAppValues(context.Background(), app, "comptoir"); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("comptoir").Get(context.Background(), "comptoir-values", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data["values.yaml"] != "replicas: 2\n" {
		t.Fatalf("the values of the application are wrong: %q", cm.Data["values.yaml"])
	}
}

// The values of an application are written again when they change.
func TestSyncAppValuesUpdatesTheValues(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "comptoir-values", Namespace: "comptoir"},
		Data:       map[string]string{"values.yaml": "replicas: 1\n"},
	})
	s := New(Config{
		Kube: client,
		GitOpsFile: func(context.Context, store.App) ([]byte, error) {
			return []byte("replicas: 2\n"), nil
		},
	})
	if err := s.syncAppValues(context.Background(), store.App{Name: "comptoir"}, "comptoir"); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("comptoir").Get(context.Background(), "comptoir-values", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data["values.yaml"] != "replicas: 2\n" {
		t.Fatalf("the values of the application are not updated: %q", cm.Data["values.yaml"])
	}
}

package control

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureBuilderCacheCreatesTheVolumeOnce(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := &Server{cfg: Config{Kube: client, BuilderNamespace: "kuberpack-build", BuilderCacheStorageClass: "selected-csi"}}
	ctx := context.Background()
	if err := s.ensureBuilderCache(ctx); err != nil {
		t.Fatal(err)
	}
	claims, err := client.CoreV1().PersistentVolumeClaims("kuberpack-build").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("the cache of the builder must have one volume, got %d", len(claims.Items))
	}
	claim := claims.Items[0]
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "selected-csi" {
		t.Fatal("builder cache ignored the platform storage class")
	}
	if claim.Name != "kuberpack-builder-cache" {
		t.Fatalf("wrong volume: %s", claim.Name)
	}
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	if size.String() != "2Gi" {
		t.Fatalf("the volume must hold the database of the vulnerabilities, got %s", size.String())
	}
	// A second call does not fail, and does not create a second volume.
	if err := s.ensureBuilderCache(ctx); err != nil {
		t.Fatal(err)
	}
	claims, err = client.CoreV1().PersistentVolumeClaims("kuberpack-build").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("the cache of the builder must keep one volume, got %d", len(claims.Items))
	}
}

func TestEnsureBuilderCacheWithoutCluster(t *testing.T) {
	s := &Server{cfg: Config{}}
	if err := s.ensureBuilderCache(context.Background()); err != nil {
		t.Fatalf("without a cluster there is nothing to create: %s", err)
	}
}

package control

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func namespace(name string, labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations}}
}

func TestProjectNamespaceMustBeOwnedOrExplicitlyAdopted(t *testing.T) {
	cases := []struct {
		name      string
		namespace *corev1.Namespace
		project   string
		wantError bool
	}{
		{
			name:      "new namespace",
			namespace: nil,
			project:   "demo",
		},
		{
			name:      "owned by the project",
			namespace: namespace("demo", map[string]string{"kuberpack.io/project": "demo"}, nil),
			project:   "demo",
		},
		{
			name:      "explicit adoption",
			namespace: namespace("legacy", nil, map[string]string{"kuberpack.io/adopt": "demo"}),
			project:   "demo",
		},
		{
			name: "historical Flux ownership",
			namespace: namespace("demo", map[string]string{
				"app.kubernetes.io/part-of":             "kuberpack",
				"kustomize.toolkit.fluxcd.io/name":      "kuberpack-projects",
				"kustomize.toolkit.fluxcd.io/namespace": "kuberpack",
			}, nil),
			project: "demo",
		},
		{
			name:      "owned by another project",
			namespace: namespace("demo", map[string]string{"kuberpack.io/project": "other"}, nil),
			project:   "demo",
			wantError: true,
		},
		{
			name:      "arbitrary existing namespace",
			namespace: namespace("demo", map[string]string{"team": "finance"}, nil),
			project:   "demo",
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			if tc.namespace != nil {
				if _, err := client.CoreV1().Namespaces().Create(context.Background(), tc.namespace, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			s := New(Config{Kube: client})
			err := s.checkNamespaceOwnership(context.Background(), tc.project, "demo")
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "not owned") {
					t.Fatalf("expected an ownership error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected ownership error: %v", err)
			}
		})
	}
}

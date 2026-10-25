package platformcli

import (
	"context"
	"encoding/base64"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// ensureFluxSource writes the GitRepository that watches the GitOps
// repository.
func ensureFluxSource(ctx context.Context, config *rest.Config, namespace, name, url, branch string) error {
	dyn, err := dynamicClientFor(config)
	if err != nil {
		return err
	}
	source := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"url":      url,
			"ref":      map[string]any{"branch": branch},
			"interval": "1m",
			"secretRef": map[string]any{
				"name": "kuberpack-gitops",
			},
		},
	}}
	return applyUnstructured(ctx, dyn, config, source)
}

// ensureFluxProjects writes the Kustomization that applies the projects of
// the GitOps repository.
func ensureFluxProjects(ctx context.Context, config *rest.Config, namespace, source, path string) error {
	dyn, err := dynamicClientFor(config)
	if err != nil {
		return err
	}
	projects := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"name": "kuberpack-projects", "namespace": namespace},
		"spec": map[string]any{
			"interval": "1m",
			"path":     path,
			"prune":    true,
			"sourceRef": map[string]any{
				"kind": "GitRepository", "name": source,
			},
		},
	}}
	return applyUnstructured(ctx, dyn, config, projects)
}

// dynamicClientFor builds a dynamic client for the Flux resources.
func dynamicClientFor(config *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(config)
}

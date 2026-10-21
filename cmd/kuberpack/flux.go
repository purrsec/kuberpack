package main

import (
	"context"
	"encoding/base64"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

// The Flux resources Kuberpack needs. They are written with the dynamic
// client: their API is not part of client-go.
var (
	gitRepositoryGVR = schema.GroupVersionResource{
		Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories",
	}
	kustomizationGVR = schema.GroupVersionResource{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations",
	}
)

// ensureFluxSource writes the GitRepository that watches the GitOps
// repository.
func ensureFluxSource(ctx context.Context, config *rest.Config, namespace, name, url, branch string) error {
	dynamicClient, err := dynamicClientFor(config)
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
	_, err = dynamicClient.Resource(gitRepositoryGVR).Namespace(namespace).
		Create(ctx, source, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	_, err = dynamicClient.Resource(gitRepositoryGVR).Namespace(namespace).
		Update(ctx, source, metav1.UpdateOptions{})
	return err
}

// ensureFluxProjects writes the Kustomization that applies the projects of
// the GitOps repository.
func ensureFluxProjects(ctx context.Context, config *rest.Config, namespace, source, path string) error {
	dynamicClient, err := dynamicClientFor(config)
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
	_, err = dynamicClient.Resource(kustomizationGVR).Namespace(namespace).
		Create(ctx, projects, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	_, err = dynamicClient.Resource(kustomizationGVR).Namespace(namespace).
		Update(ctx, projects, metav1.UpdateOptions{})
	return err
}

// dynamicClientFor builds a dynamic client for the Flux resources.
func dynamicClientFor(config *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(config)
}

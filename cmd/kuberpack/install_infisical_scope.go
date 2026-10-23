package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsYaml "sigs.k8s.io/yaml"
)

func (i *installation) infisicalScopePlatform() promote.Platform {
	return promote.Platform{ProjectsDir: i.c.ProjectsPath(), ControlPlaneNamespace: i.c.Namespace,
		BuildNamespace: i.c.BuildNamespace, InfisicalNamespace: infisicalNamespace, InfisicalOperatorScope: i.c.Infisical}
}

// seedInfisicalOperatorScope makes the values available before either Helm or
// Flux starts the operator. Project namespaces must exist before Helm can
// create their namespaced Roles, including on a replacement cluster.
func (i *installation) seedInfisicalOperatorScope(ctx context.Context) (map[string]any, error) {
	p := i.infisicalScopePlatform()
	if err := promote.SyncInfisicalOperatorScope(i.appsDir(), p); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(i.appsDir(), filepath.FromSlash(p.ProjectsDirPath()), promote.InfisicalOperatorScopeFile))
	if err != nil {
		return nil, err
	}
	var desired corev1.ConfigMap
	if err := sigsYaml.Unmarshal(data, &desired); err != nil {
		return nil, err
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(desired.Data["values.yaml"]), &values); err != nil {
		return nil, err
	}
	namespaces, err := promote.InfisicalOperatorNamespaces(i.appsDir(), p)
	if err != nil {
		return nil, err
	}
	for _, namespace := range namespaces {
		if err := ensureNamespace(ctx, i.kube, namespace); err != nil {
			return nil, err
		}
	}
	client := i.kube.CoreV1().ConfigMaps(i.c.Namespace)
	current, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, &desired, metav1.CreateOptions{})
	} else if err == nil {
		// Keep Flux inventory metadata so a no-op reinstall cannot remove its
		// ownership. The generated values contain no credentials.
		changed := !reflect.DeepEqual(current.Data, desired.Data)
		current.Data = desired.Data
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		for name, value := range desired.Labels {
			changed = changed || current.Labels[name] != value
			current.Labels[name] = value
		}
		if changed {
			_, err = client.Update(ctx, current, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("Infisical operator scope: %w", err)
	}
	return values, nil
}

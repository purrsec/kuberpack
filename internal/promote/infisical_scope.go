package promote

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	InfisicalOperatorScopeName = "kuberpack-infisical-operator-scope"
	InfisicalOperatorScopeFile = "infisical-operator-scope.yaml"
)

// InfisicalOperatorNamespaces derives access from declared projects rather than
// observing every namespace in the cluster. Invalid manifests fail closed.
func InfisicalOperatorNamespaces(work string, p Platform) ([]string, error) {
	p = p.withDefaults()
	if err := rejectRepoSymlinks(work, p.ProjectsDirPath()); err != nil {
		return nil, err
	}
	seen := map[string]bool{p.InfisicalNamespace: true}
	entries, err := os.ReadDir(filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath())))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath()), entry.Name(), "kuberpack.yaml"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		manifest, err := ParseProjectManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("Infisical scope for project %s: %w", entry.Name(), err)
		}
		if sanitizeName(manifest.Project) != entry.Name() {
			return nil, fmt.Errorf("Infisical scope: manifest project does not match directory %s", entry.Name())
		}
		namespace := projectNamespace(manifest, entry.Name())
		if err := validateInfisicalProjectNamespace(namespace, p); err != nil {
			return nil, err
		}
		seen[namespace] = true
	}
	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

func validateInfisicalProjectNamespace(namespace string, p Platform) error {
	if errs := validation.IsDNS1123Label(namespace); len(errs) != 0 {
		return fmt.Errorf("invalid Infisical project namespace %q", namespace)
	}
	if strings.HasPrefix(namespace, "kube-") || namespace == "default" || namespace == "flux-system" || namespace == p.BuildNamespace || namespace == p.ControlPlaneNamespace || namespace == p.InfisicalNamespace {
		return fmt.Errorf("Infisical operator must not receive project access to protected namespace %q", namespace)
	}
	return nil
}

// RenderInfisicalOperatorScope emits non-secret Helm values in the control
// namespace. The chart requires both scopedRBAC and a nonempty namespace list.
func RenderInfisicalOperatorScope(p Platform, namespaces []string) ([]byte, error) {
	p = p.withDefaults()
	if len(namespaces) == 0 {
		return nil, fmt.Errorf("Infisical operator scope must not be empty")
	}
	values, err := yaml.Marshal(map[string]any{"scopedRBAC": true, "scopedNamespaces": namespaces})
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": InfisicalOperatorScopeName, "namespace": p.ControlPlaneNamespace,
			"labels": map[string]string{"reconcile.fluxcd.io/watch": "Enabled", "app.kubernetes.io/part-of": "kuberpack"}},
		"data": map[string]string{"values.yaml": string(values)},
	})
}

// SyncInfisicalOperatorScope updates the access list in the same GitOps commit
// as project creation/removal. Flux updates Helm's --namespaces and scoped RBAC.
func SyncInfisicalOperatorScope(work string, p Platform) error {
	if !p.InfisicalOperatorScope {
		return nil
	}
	p = p.withDefaults()
	namespaces, err := InfisicalOperatorNamespaces(work, p)
	if err != nil {
		return err
	}
	data, err := RenderInfisicalOperatorScope(p, namespaces)
	if err != nil {
		return err
	}
	root := filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath()))
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, InfisicalOperatorScopeFile), data, 0644); err != nil {
		return err
	}
	parent := filepath.Join(work, filepath.FromSlash(p.ProjectsParentPath()))
	raw, err := os.ReadFile(parent)
	if os.IsNotExist(err) {
		raw = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	} else if err != nil {
		return err
	}
	if !listedInKustomization(raw, InfisicalOperatorScopeFile) {
		raw = appendKustomizationResource(raw, InfisicalOperatorScopeFile)
		return os.WriteFile(parent, raw, 0644)
	}
	return nil
}

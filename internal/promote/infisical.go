package promote

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ensureSecretCR writes an InfisicalStaticSecret for the app's declared
// Infisical keys and makes sure the parent kustomization lists it. Returns the
// paths touched and whether anything changed. No-op when the platform has no
// Infisical project or the app declares no secrets.
func ensureSecretCR(work string, spec AppSpec, p Platform) ([]string, bool, error) {
	p = p.withDefaults()
	if !p.InfisicalEnabled() || len(spec.Secrets) == 0 {
		return nil, false, nil
	}
	crRel := p.InfisicalCRPath(infisicalObjectName(spec))
	parentRel := p.InfisicalParentPath()
	crPath := filepath.Join(work, filepath.FromSlash(crRel))
	if err := os.MkdirAll(filepath.Dir(crPath), 0o755); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(crPath, []byte(infisicalCRContent(spec, p)), 0o644); err != nil {
		return nil, false, err
	}

	// The root kustomization of the repository must list the directory of
	// the Infisical objects: Flux applies the root, and the objects would
	// stay in the repository without ever reaching the cluster.
	if err := ensureInfisicalInRootKustomization(work, p); err != nil {
		return nil, false, err
	}

	parentPath := filepath.Join(work, filepath.FromSlash(parentRel))
	raw, err := os.ReadFile(parentPath)
	if os.IsNotExist(err) {
		raw = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	} else if err != nil {
		return nil, false, err
	}
	// The resource must carry the .yaml extension: kustomize treats an entry
	// without one as a directory. Also drop a legacy extensionless entry.
	resource := filepath.Base(crRel)
	legacy := strings.TrimSuffix(resource, ".yaml")
	if listedInKustomization(raw, legacy) {
		raw = removeKustomizationResource(raw, legacy)
	}
	if !listedInKustomization(raw, resource) {
		raw = appendKustomizationResource(raw, resource)
	}
	if err := os.WriteFile(parentPath, raw, 0o644); err != nil {
		return nil, false, err
	}
	return []string{crRel, parentRel}, true, nil
}

// ensureInfisicalInRootKustomization lists the directory of the Infisical
// objects in the root kustomization of the repository.
//
// The root kustomization is the bootstrap of the platform: it is written by
// hand. The directory of the Infisical objects, on the other hand, is
// created by the platform, and Flux only applies what the root lists.
func ensureInfisicalInRootKustomization(work string, p Platform) error {
	p = p.withDefaults()
	rootPath := filepath.Join(work, "kustomization.yaml")
	raw, err := os.ReadFile(rootPath)
	if err != nil {
		// No root kustomization: the repository is not the one of the
		// platform, there is nothing to list.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entry := filepath.ToSlash(p.InfisicalDir)
	if listedInKustomization(raw, entry) {
		return nil
	}
	return os.WriteFile(rootPath, appendKustomizationResource(raw, entry), 0o644)
}

// removeSecretCR deletes the app's InfisicalStaticSecret and its parent entry.
// No-op when the platform has no Infisical project.
func removeSecretCR(work string, spec AppSpec, p Platform) ([]string, bool, error) {
	p = p.withDefaults()
	if !p.InfisicalEnabled() {
		return nil, false, nil
	}
	crRel := p.InfisicalCRPath(infisicalObjectName(spec))
	parentRel := p.InfisicalParentPath()
	crPath := filepath.Join(work, filepath.FromSlash(crRel))
	var paths []string
	changed := false
	if _, statErr := os.Stat(crPath); statErr == nil {
		if err := os.Remove(crPath); err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
		paths = append(paths, crRel)
		changed = true
	} else if !os.IsNotExist(statErr) {
		return nil, false, statErr
	}

	parentPath := filepath.Join(work, filepath.FromSlash(parentRel))
	if raw, err := os.ReadFile(parentPath); err == nil {
		updated := raw
		for _, resource := range []string{filepath.Base(crRel), strings.TrimSuffix(filepath.Base(crRel), ".yaml")} {
			if listedInKustomization(updated, resource) {
				updated = removeKustomizationResource(updated, resource)
			}
		}
		if string(updated) != string(raw) {
			if err := os.WriteFile(parentPath, updated, 0o644); err != nil {
				return nil, false, err
			}
			paths = append(paths, parentRel)
			changed = true
		}
		var parent struct {
			Resources []string `yaml:"resources"`
		}
		if err := yaml.Unmarshal(updated, &parent); err != nil {
			return nil, false, err
		}
		if len(parent.Resources) == 0 {
			// An empty legacy tree is not a buildable kustomization. Its
			// root reference must leave in the same commit as its last sync.
			rootPath := filepath.Join(work, "kustomization.yaml")
			root, err := os.ReadFile(rootPath)
			if err == nil && listedInKustomization(root, p.InfisicalDir) {
				if err := os.WriteFile(rootPath, removeKustomizationResource(root, p.InfisicalDir), 0o644); err != nil {
					return nil, false, err
				}
				paths = append(paths, "kustomization.yaml")
				changed = true
			} else if err != nil && !os.IsNotExist(err) {
				return nil, false, err
			}
		}
	}
	return paths, changed, nil
}

// infisicalObjectName is the name of the generated InfisicalStaticSecret.
// A service of a project is scoped by its project: two projects may declare
// a service with the same name.
func infisicalObjectName(spec AppSpec) string {
	if project := strings.TrimSpace(spec.Project); project != "" {
		return AppRuntimeSecretName(sanitizeName(project) + "-" + spec.Name)
	}
	return AppRuntimeSecretName(spec.Name)
}

func infisicalCRContent(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack. The values stay in Infisical; this object only references keys.\n")
	b.WriteString("apiVersion: secrets.infisical.com/v1beta1\n")
	b.WriteString("kind: InfisicalStaticSecret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + infisicalObjectName(spec) + "\n")
	b.WriteString("  namespace: " + p.InfisicalNamespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  infisicalAuthRef:\n")
	b.WriteString("    name: " + p.InfisicalAuthRef + "\n")
	b.WriteString("    namespace: " + p.InfisicalNamespace + "\n")
	b.WriteString("  sources:\n")
	b.WriteString("    - projectId: " + p.InfisicalProjectID + "\n")
	b.WriteString("      environmentSlug: " + strconv.Quote(secretEnv(spec)) + "\n")
	b.WriteString("      secretPath: " + p.InfisicalPath + "\n")
	b.WriteString("  syncOptions:\n")
	b.WriteString("    refreshInterval: 1m\n")
	b.WriteString("  targets:\n")
	b.WriteString("    - kind: Secret\n")
	b.WriteString("      name: " + AppRuntimeSecretName(spec.Name) + "\n")
	b.WriteString("      namespace: " + releaseNamespace(spec, p) + "\n")
	b.WriteString("      creationPolicy: Orphan\n")
	b.WriteString("      template:\n")
	b.WriteString("        engineVersion: v1\n")
	b.WriteString("        data:\n")
	for _, key := range spec.Secrets {
		b.WriteString(fmt.Sprintf("          %s: '{{ (index . %q).Value }}'\n", key, key))
	}
	return b.String()
}

func secretEnv(spec AppSpec) string {
	if env := strings.TrimSpace(spec.SecretEnv); env != "" {
		return env
	}
	return "prod"
}

// normalizeSecretKeys trims, drops empties and de-duplicates while keeping order.
func normalizeSecretKeys(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// NormalizeSecretKeys trims, drops empties and de-duplicates while keeping order.
func NormalizeSecretKeys(keys []string) []string {
	return normalizeSecretKeys(keys)
}

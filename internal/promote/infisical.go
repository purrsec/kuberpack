package promote

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// InfisicalAutoReloadAnnotation est l'annotation lue par l'operateur
// Infisical sur un workload. Quand elle vaut "true", l'operateur pose l'etag
// du Secret synchronisé sur le Deployment et sur son template de pod : le
// changement de template déclenche un rollout, donc les pods existants
// relisent les nouvelles valeurs.
//
// C'est la fonctionnalité native de l'operateur (version épinglée 0.11.13) :
// elle ne demande ni contrôleur supplémentaire, ni droit de lecture des
// Secrets pour le control plane. L'operateur a déjà, dans les namespaces de
// projets, le droit de mettre à jour les Deployments.
const InfisicalAutoReloadAnnotation = "secrets.infisical.com/auto-reload"

// secretRotationEnabled indique si le service doit être redémarré par
// l'operateur quand son Secret synchronisé change. Seul un service de projet
// qui déclare des clés consomme un InfisicalStaticSecret : une preview, une
// application autonome ou un service sans secret ne portent pas
// l'annotation, et ne sont donc jamais redémarrés par une rotation.
func secretRotationEnabled(spec AppSpec, p Platform) bool {
	if spec.Preview || strings.TrimSpace(spec.Project) == "" || len(spec.Secrets) == 0 {
		return false
	}
	return p.InfisicalEnabled()
}

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
	projectID, authRef, secretPath := infisicalScope(spec, p)
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack. The values stay in Infisical; this object only references keys.\n")
	b.WriteString("apiVersion: secrets.infisical.com/v1beta1\n")
	b.WriteString("kind: InfisicalStaticSecret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + infisicalObjectName(spec) + "\n")
	b.WriteString("  namespace: " + p.InfisicalNamespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  infisicalAuthRef:\n")
	b.WriteString("    name: " + authRef + "\n")
	b.WriteString("    namespace: " + p.InfisicalNamespace + "\n")
	b.WriteString("  sources:\n")
	b.WriteString("    - projectId: " + projectID + "\n")
	b.WriteString("      environmentSlug: " + strconv.Quote(secretEnv(spec)) + "\n")
	b.WriteString("      secretPath: " + secretPath + "\n")
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
		fmt.Fprintf(&b, "          %s: '{{ (index . %q).Value }}'\n", key, key)
	}
	return b.String()
}

// infisicalScope applies the optional per-project override. An empty field
// keeps the platform-wide value, so existing manifests do not change.
func infisicalScope(spec AppSpec, p Platform) (projectID, authRef, secretPath string) {
	projectID, authRef, secretPath = p.InfisicalProjectID, p.InfisicalAuthRef, p.InfisicalPath
	if value := strings.TrimSpace(spec.InfisicalProjectID); value != "" {
		projectID = value
	}
	if value := strings.TrimSpace(spec.InfisicalAuthRef); value != "" {
		authRef = value
	}
	if value := strings.TrimSpace(spec.InfisicalPath); value != "" {
		secretPath = value
	}
	if p.RequireProjectSecrets {
		// withDefaults fills the platform identity and path. In strict mode
		// they must not leak into a project: derive both from the project.
		authRef = "kuberpack-" + sanitizeName(spec.Project)
		secretPath = "/projects/" + sanitizeName(spec.Project)
	}
	return projectID, authRef, secretPath
}

// validateProjectSecretScope makes the per-project boundary enforceable when
// the installation requires it: a service that declares keys must reference its
// own Infisical project, and cannot point at another project's identity/path.
func validateProjectSecretScope(m ProjectManifest, p Platform) error {
	if !p.RequireProjectSecrets {
		return nil
	}
	hasSecrets := false
	for _, svc := range m.Services {
		if len(svc.Secrets) > 0 {
			hasSecrets = true
			break
		}
	}
	if !hasSecrets {
		return nil
	}
	if m.Infisical == nil || strings.TrimSpace(m.Infisical.ProjectID) == "" {
		return fmt.Errorf("project %q declares secrets without its own Infisical project; set infisical.project_id (and auth_ref/path if needed)", m.Project)
	}
	expectedAuth := "kuberpack-" + sanitizeName(m.Project)
	expectedPath := "/projects/" + sanitizeName(m.Project)
	if auth := strings.TrimSpace(m.Infisical.AuthRef); auth != "" && auth != expectedAuth {
		return fmt.Errorf("project %q must use its own Infisical identity %q, not %q", m.Project, expectedAuth, auth)
	}
	if secretPath := strings.TrimSpace(m.Infisical.Path); secretPath != "" && secretPath != expectedPath {
		return fmt.Errorf("project %q must use its own Infisical path %q, not %q", m.Project, expectedPath, secretPath)
	}
	return nil
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

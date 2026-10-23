package promote

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func guardProjectTree(work string, p Platform) error {
	p = p.withDefaults()
	return rejectRepoSymlinks(work, p.ProjectsDirPath(), p.InfisicalDir)
}

// A namespace is owned by exactly one project. A directory name alone is
// not a Kubernetes isolation boundary, and renaming a namespace is a migration.
func validateProjectNamespace(work string, m ProjectManifest, p Platform) error {
	namespace := projectNamespace(m, m.Project)
	for _, protected := range []string{p.ControlPlaneNamespace, p.SourceNamespace, p.InfisicalNamespace, p.BuildNamespace} {
		if protected != "" && namespace == protected {
			return fmt.Errorf("namespace %q is reserved for the platform", namespace)
		}
	}
	if reservedProjectNamespace(namespace) {
		return fmt.Errorf("namespace %q is reserved for the platform", namespace)
	}
	entries, err := os.ReadDir(filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath())))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath()), entry.Name(), "kuberpack.yaml"))
		if os.IsNotExist(err) {
			continue
		} // A hand-managed Helm/Kustomize directory.
		if err != nil {
			return err
		}
		previous, err := ParseProjectManifest(raw)
		if err != nil {
			return fmt.Errorf("project directory %q: %w", entry.Name(), err)
		}
		if previous.Project != entry.Name() {
			return fmt.Errorf("project %q does not match its directory %q", previous.Project, entry.Name())
		}
		oldNamespace := projectNamespace(previous, previous.Project)
		if previous.Project == m.Project {
			if oldNamespace != namespace {
				return fmt.Errorf("project %q already owns namespace %q; changing it requires an explicit data migration", m.Project, oldNamespace)
			}
		} else if oldNamespace == namespace {
			return fmt.Errorf("namespace %q is already owned by project %q", namespace, previous.Project)
		}
		if previous.Project != m.Project {
			identities := map[string]bool{}
			for service := range previous.Services {
				identities[sanitizeName(previous.Project+"-"+service)] = true
			}
			for service := range m.Services {
				if identities[sanitizeName(m.Project+"-"+service)] {
					return fmt.Errorf("application identity %q is already owned by project %q; choose a different project or service name", m.Project+"-"+service, previous.Project)
				}
			}
		}
	}
	return nil
}

func servicePeerNames(m ProjectManifest, svc Service) []string {
	if svc.Peers != nil {
		return *svc.Peers
	}
	return m.Policies.Peers
}

func incomingServicePeers(m ProjectManifest, target string) []string {
	var incoming []string
	for source, svc := range m.Services {
		for _, peer := range servicePeerNames(m, svc) {
			if peer == target {
				incoming = append(incoming, source)
				break
			}
		}
	}
	sort.Strings(incoming)
	return incoming
}

func addonServiceClients(m ProjectManifest, target string) []string {
	var clients []string
	for source, svc := range m.Services {
		for _, peer := range append(append([]string{}, servicePeerNames(m, svc)...), svc.Addons...) {
			if peer == target {
				clients = append(clients, source)
				break
			}
		}
	}
	sort.Strings(clients)
	return clients
}

func addonDependencyClients(m ProjectManifest, target string) []string {
	var clients []string
	for name, addon := range m.Addons {
		if addon.Backup != nil && addon.Backup.S3 == target {
			clients = append(clients, name)
		}
	}
	sort.Strings(clients)
	return clients
}

// Isolate ingress even for helper pods and hand-managed charts. Workload
// policies additionally isolate application egress. Addon operators retain
// their documented control-plane egress until engine-specific rules exist.
func RenderProjectDefaultDeny(namespace string) string {
	return "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: kuberpack-default-deny\n  namespace: " + namespace + "\nspec:\n  podSelector: {}\n  policyTypes: [Ingress]\n  ingress: []\n"
}

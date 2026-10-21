package promote

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// AddonSpec is what the manifest declares about an addon.
type AddonSpec struct {
	// Name is the name of the addon in the manifest.
	Name string
	// Project is the project the addon belongs to.
	Project string
	// Namespace is the namespace of the project.
	Namespace string
	// Database is the name of the database to create.
	Database string
	// Owner is the owner of the database.
	Owner string
	// Backup is the backup policy of the addon.
	Backup *Backup
}

// AddonEnv is an environment variable an addon provides to the services that
// use it. The value stays in a Kubernetes Secret: Kuberpack only writes the
// reference.
type AddonEnv struct {
	// Name is the name of the environment variable.
	Name string
	// SecretName is the Secret that holds the value.
	SecretName string
	// SecretKey is the key of the value in the Secret.
	SecretKey string
	// Value is a literal value, when the addon does not need a Secret.
	Value string
}

// AddonResult is what an addon gives to the services that use it.
type AddonResult struct {
	// Env are the environment variables the addon provides.
	Env []AddonEnv
	// Kind is the kind of the addon (postgres, valkey). It labels the pods
	// of the addon, and the NetworkPolicy selects them by that label.
	Kind string
	// Port is the port the addon listens on.
	Port int
	// Secret is the name of the Secret of the addon, when it has one.
	Secret string
}

// AddonEngine renders an infrastructure dependency of a project.
//
// An engine is a self-contained renderer: it writes the Kubernetes objects of
// the addon and declares what the services that use it need. Adding a type
// of addon is a new engine, not a change in the compiler.
type AddonEngine interface {
	// Name is the name of the engine in the manifest.
	Name() string
	// Render writes the objects of the addon in dir and returns what the
	// services that use it need.
	Render(dir string, spec AddonSpec, platform Platform) (AddonResult, error)
}

// addonEngines holds the available engines.
var addonEngines = map[string]AddonEngine{
	"cnpg":   cnpgEngine{},
	"valkey": valkeyEngine{},
}

// AddonEngineNames lists the available engines.
func AddonEngineNames() []string {
	out := make([]string, 0, len(addonEngines))
	for name := range addonEngines {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AddonEngineFor returns the engine of a manifest.
func AddonEngineFor(name string) (AddonEngine, bool) {
	engine, ok := addonEngines[strings.ToLower(strings.TrimSpace(name))]
	return engine, ok
}

// RegisterAddonEngine makes an engine available to the manifests. It is the
// only place where an addon type enters the product.
func RegisterAddonEngine(engine AddonEngine) {
	addonEngines[strings.ToLower(engine.Name())] = engine
}

// addonKustomization is the kustomization of an addon directory.
func addonKustomization(resources ...string) string {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("\n")
	b.WriteString("resources:\n")
	for _, resource := range resources {
		b.WriteString("  - " + resource + "\n")
	}
	return b.String()
}

// addonFile is a file of an addon, written by its engine.
type addonFile struct {
	name    string
	content string
}

// writeAddonFiles writes the objects of an addon in its directory.
func writeAddonFiles(dir string, files ...addonFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, []byte(file.content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// envYAMLForAddon renders the environment variables an addon provides.
func envYAMLForAddon(envs []AddonEnv) string {
	var b strings.Builder
	for _, env := range envs {
		if strings.TrimSpace(env.Name) == "" {
			continue
		}
		b.WriteString("- name: " + env.Name + "\n")
		if env.Value != "" {
			b.WriteString("  value: " + strconv.Quote(env.Value) + "\n")
			continue
		}
		b.WriteString("  valueFrom:\n")
		b.WriteString("    secretKeyRef:\n")
		b.WriteString("      name: " + env.SecretName + "\n")
		b.WriteString("      key: " + env.SecretKey + "\n")
	}
	return b.String()
}

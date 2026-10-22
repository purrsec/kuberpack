package promote

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed addons/*.yaml
var addonCatalogFS embed.FS

// ResourceList is a quantity of CPU and of memory.
type ResourceList struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

// AddonResources are the resources of an addon.
type AddonResources struct {
	Requests ResourceList `yaml:"requests"`
	Limits   ResourceList `yaml:"limits"`
}

// AddonPlan is a size of an addon: how many instances, how much storage,
// which resources.
type AddonPlan struct {
	Instances int            `yaml:"instances"`
	Storage   string         `yaml:"storage"`
	Resources AddonResources `yaml:"resources"`
	// MaxMemory is the memory a cache may use. A cache without a limit
	// eats the memory of the node, and the neighbour pays for it.
	MaxMemory string `yaml:"maxmemory"`
}

// AddonEntry is a type of addon in the catalogue: what it is, what it
// listens on, and the contract it offers to the applications.
//
// The catalogue holds in data what every engine used to repeat in Go. A
// type of addon is a file: the defaults are written once, and the
// manifest chooses a plan and overrides what it needs.
type AddonEntry struct {
	// Type is the name of the type, the one the manifest names.
	Type string `yaml:"type"`
	// Kind is the family of the addon: it labels the pods, and the
	// network policy selects them by that label.
	Kind string `yaml:"kind"`
	// Port is the port the addon listens on.
	Port int `yaml:"port"`
	// Image is the image of the addon, pinned by digest.
	Image string `yaml:"image"`
	// Versions are the versions of the addon the manifest can ask for,
	// with the image of each one. The key is the version, the value is
	// the image, pinned by digest.
	//
	// The version of a database is the version of the software: a
	// PostgreSQL 14 and a PostgreSQL 16 do not share their data
	// directory, and the application that needs one of them says so.
	Versions map[string]string `yaml:"versions"`
	// DefaultVersion is the version the manifest gets when it does not
	// ask for one.
	DefaultVersion string `yaml:"defaultVersion"`
	// Plans are the sizes the manifest can ask for.
	Plans map[string]AddonPlan `yaml:"plans"`
	// Env is the contract: the variables an application receives, and the
	// key of the secret that holds them. The values never pass through
	// Kuberpack.
	Env map[string]string `yaml:"env"`
	// Operator is the operator the type needs in the cluster: a type
	// that writes the resources of an operator is refused when the
	// platform does not declare it.
	Operator string `yaml:"operator"`
	// Engine is the engine that renders this type. The manifest may name
	// the type or the engine.
	Engine string `yaml:"engine"`
	// Bootstrap describes the Job that creates the credentials of the
	// type, in the cluster.
	Bootstrap AddonBootstrap `yaml:"bootstrap"`
}

// AddonBootstrap describes how the credentials of a type are born.
type AddonBootstrap struct {
	// Image is the image of the Job: it talks to the API of the cluster.
	Image string `yaml:"image"`
	// Secret is the name of the secret the Job creates.
	Secret string `yaml:"secret"`
	// Keys are the keys of the secret.
	Keys []string `yaml:"keys"`
}

// AddonCatalog is the catalogue of the addons of the platform.
type AddonCatalog struct {
	entries map[string]AddonEntry
	types   []string
}

// catalog is the catalogue of the platform, read from the files that
// travel with the binary.
var catalog = loadAddonCatalog()

func loadAddonCatalog() *AddonCatalog {
	out := &AddonCatalog{entries: map[string]AddonEntry{}}
	files, err := addonCatalogFS.ReadDir("addons")
	if err != nil {
		return out
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		raw, err := addonCatalogFS.ReadFile(path.Join("addons", file.Name()))
		if err != nil {
			continue
		}
		var entry AddonEntry
		if err := yaml.Unmarshal(raw, &entry); err != nil {
			continue
		}
		if entry.Type == "" {
			continue
		}
		out.entries[entry.Type] = entry
		out.types = append(out.types, entry.Type)
	}
	sort.Strings(out.types)
	return out
}

// Entry returns the entry of a type of addon.
func (c *AddonCatalog) Entry(name string) (AddonEntry, bool) {
	entry, ok := c.entries[strings.ToLower(strings.TrimSpace(name))]
	return entry, ok
}

// Types lists the types of the catalogue.
func (c *AddonCatalog) Types() []string {
	return append([]string{}, c.types...)
}

// EntryForEngine returns the entry of the type an engine renders. The
// manifest may name the type ("postgres") or the engine ("cnpg").
func (c *AddonCatalog) EntryForEngine(name string) (AddonEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, entry := range c.entries {
		if strings.EqualFold(entry.Engine, name) {
			return entry, true
		}
	}
	return c.Entry(name)
}

// AddonCatalogEntryForEngine returns the entry of the type an engine
// renders.
func AddonCatalogEntryForEngine(name string) (AddonEntry, bool) {
	return catalog.EntryForEngine(name)
}

// AddonCatalogEntry returns the entry of a type of addon of the platform.
func AddonCatalogEntry(name string) (AddonEntry, bool) {
	return catalog.Entry(name)
}

// AddonCatalogTypes lists the types of addon the platform knows.
func AddonCatalogTypes() []string {
	return catalog.Types()
}

// ImageForVersion returns the image of a version of the addon, and the
// version itself.
//
// A version the catalogue does not know is refused: the platform does not
// guess an image, and it does not run something else than what was
// reviewed.
func (e AddonEntry) ImageForVersion(version string) (string, string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = strings.TrimSpace(e.DefaultVersion)
	}
	if version == "" {
		return e.Image, "", nil
	}
	image, ok := e.Versions[version]
	if !ok {
		return "", "", fmt.Errorf("the addon %q does not have the version %q (%s)",
			e.Type, version, strings.Join(e.VersionNames(), ", "))
	}
	return image, version, nil
}

// VersionNames lists the versions the catalogue holds for the addon.
func (e AddonEntry) VersionNames() []string {
	out := make([]string, 0, len(e.Versions))
	for name := range e.Versions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Plan resolves the size of an addon: the plan of the catalogue the
// manifest asks for, overridden by the parameters it gives.
//
// The plan is a name, not a number: "small" and "medium" are the sizes
// the platform supports, and they are the same everywhere.
func (e AddonEntry) Plan(name string, params map[string]string) (AddonPlan, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "small"
	}
	plan, ok := e.Plans[name]
	if !ok {
		return AddonPlan{}, fmt.Errorf("the plan %q of the addon %q does not exist (%s)",
			name, e.Type, strings.Join(e.PlanNames(), ", "))
	}
	if len(params) == 0 {
		return plan, nil
	}
	overrides := map[string]string{}
	for key, value := range params {
		overrides[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	if raw, ok := overrides["instances"]; ok {
		instances, err := parseCount(raw)
		if err != nil {
			return AddonPlan{}, fmt.Errorf("the addon %q: %w", e.Type, err)
		}
		plan.Instances = instances
	}
	if raw, ok := overrides["storage"]; ok {
		plan.Storage = raw
	}
	if raw, ok := overrides["cpu"]; ok {
		plan.Resources.Requests.CPU = raw
		plan.Resources.Limits.CPU = raw
	}
	if raw, ok := overrides["memory"]; ok {
		plan.Resources.Requests.Memory = raw
		plan.Resources.Limits.Memory = raw
	}
	return plan, nil
}

// PlanNames lists the plans of an addon.
func (e AddonEntry) PlanNames() []string {
	out := make([]string, 0, len(e.Plans))
	for name := range e.Plans {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// parseCount reads a number of instances.
func parseCount(raw string) (int, error) {
	count := 0
	for _, r := range strings.TrimSpace(raw) {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("the number of instances %q is not a number", raw)
		}
		count = count*10 + int(r-'0')
	}
	if count <= 0 {
		return 0, fmt.Errorf("the number of instances %q is not positive", raw)
	}
	return count, nil
}

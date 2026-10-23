package promote

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

var environmentSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var capabilityPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// These values are emitted as plain YAML by the addon renderers. Confirm that
// decoding a scalar preserves exactly the submitted string, including its type;
// a newline, comment, alias, tag or mapping must not become generated structure.
func validatePlainScalar(field, value string) error {
	if value == "" {
		return nil
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
		return fmt.Errorf("%s must be a single-line value without control characters", field)
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte("value: "+value+"\n"), &decoded); err != nil || len(decoded) != 1 || decoded["value"] != value {
		return fmt.Errorf("%s must be a plain string without YAML syntax", field)
	}
	return nil
}

func reservedProjectNamespace(namespace string) bool {
	return namespace == "default" || strings.HasPrefix(namespace, "kube-") ||
		namespace == "kuberpack" || namespace == "kuberpack-build" || namespace == "flux-system" ||
		namespace == "cert-manager" || namespace == "infisical" || namespace == "infisical-operator" ||
		namespace == "cnpg-system" || namespace == "barman-cloud"
}

// Validate the human source before generating files or contacting a registry.
// Invalid input must not become a successful compile followed by a Flux error.
func validateManifestFields(m ProjectManifest) error {
	if errs := validation.IsDNS1123Label(m.Project); len(errs) > 0 {
		return fmt.Errorf("project %q: %s", m.Project, strings.Join(errs, "; "))
	}
	if reservedProjectNamespace(projectNamespace(m, m.Project)) {
		return fmt.Errorf("namespace %q is reserved for the platform or Kubernetes", projectNamespace(m, m.Project))
	}
	if m.Namespace != "" {
		if errs := validation.IsDNS1123Label(m.Namespace); len(errs) > 0 {
			return fmt.Errorf("namespace %q: %s", m.Namespace, strings.Join(errs, "; "))
		}
	}
	for name, addon := range m.Addons {
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
			return fmt.Errorf("addon %q: %s", name, strings.Join(errs, "; "))
		}
		if _, exists := m.Services[name]; exists {
			return fmt.Errorf("%q is both a service and an addon; use distinct names", name)
		}
		for field, value := range map[string]string{"database": addon.Database, "owner": addon.Owner, "path": addon.Path} {
			if err := validatePlainScalar(fmt.Sprintf("addon %q: %s", name, field), value); err != nil {
				return err
			}
		}
		if addon.Path != "" && (!path.IsAbs(addon.Path) || path.Clean(addon.Path) != addon.Path || addon.Path == "/" || addon.Path == "/tmp") {
			return fmt.Errorf("addon %q: path must be an absolute mount path other than / or /tmp", name)
		}
		if addon.Backup != nil {
			if err := validatePlainScalar(fmt.Sprintf("addon %q: backup.retention", name), addon.Backup.Retention); err != nil {
				return err
			}
		}
		for field, value := range addon.Params {
			switch field {
			case "instances":
				count, err := strconv.ParseInt(value, 10, 32)
				if err != nil || count <= 0 {
					return fmt.Errorf("addon %q: params.instances must be a positive 32-bit integer", name)
				}
			case "storage", "cpu", "memory":
				quantity, err := resource.ParseQuantity(value)
				if err != nil || quantity.Sign() <= 0 {
					return fmt.Errorf("addon %q: params.%s must be a positive Kubernetes quantity", name, field)
				}
			default:
				return fmt.Errorf("addon %q: unsupported parameter %q", name, field)
			}
		}
	}
	for name, svc := range m.Services {
		if errs := validation.IsDNS1123Label(name); len(errs) > 0 || len(name) > 53 {
			return fmt.Errorf("service %q must be a lowercase DNS label of at most 53 characters", name)
		}
		if svc.Hostname != "" {
			if errs := validation.IsDNS1123Subdomain(svc.Hostname); len(errs) > 0 {
				return fmt.Errorf("service %q: hostname must be a DNS hostname", name)
			}
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return fmt.Errorf("service %q: port must be between 1 and 65535 (or omitted)", name)
		}
		if svc.Replicas != nil && *svc.Replicas < 0 {
			return fmt.Errorf("service %q: replicas must be zero or greater", name)
		}
		if svc.StartupBudget != 0 && (svc.StartupBudget < 2 || svc.StartupBudget > 86400) {
			return fmt.Errorf("service %q: startup_budget must be between 2 and 86400 seconds", name)
		}
		if svc.Healthcheck != "" && !strings.HasPrefix(svc.Healthcheck, "/") {
			return fmt.Errorf("service %q: healthcheck must start with /", name)
		}
		if strings.IndexFunc(svc.Healthcheck, unicode.IsControl) >= 0 || strings.ContainsAny(svc.Healthcheck, "\u2028\u2029") {
			return fmt.Errorf("service %q: healthcheck must not contain control characters or line breaks", name)
		}
		if svc.SecretEnv != "" && !environmentSlugPattern.MatchString(svc.SecretEnv) {
			return fmt.Errorf("service %q: secret_env must be an environment slug of at most 64 lowercase letters, digits, underscores or hyphens", name)
		}
		for _, capability := range svc.Capabilities {
			if !capabilityPattern.MatchString(capability) {
				return fmt.Errorf("service %q: capability %q must be an uppercase Linux capability name", name, capability)
			}
		}
		if svc.Build != nil {
			root := svc.Build.RootDirectory
			if path.IsAbs(root) || strings.Contains(root, "\\") || root == ".." || strings.HasPrefix(path.Clean(root), "../") || strings.IndexFunc(root, unicode.IsControl) >= 0 {
				return fmt.Errorf("service %q: build.root_directory must stay within the repository", name)
			}
		}
		for key := range svc.Env {
			if errs := validation.IsEnvVarName(key); len(errs) > 0 {
				return fmt.Errorf("service %q: invalid environment variable %q", name, key)
			}
		}
		for _, key := range svc.Secrets {
			if errs := validation.IsEnvVarName(key); len(errs) > 0 {
				return fmt.Errorf("service %q: invalid secret key %q", name, key)
			}
			if _, exists := svc.Env[key]; exists {
				return fmt.Errorf("service %q: %s is declared in both env and secrets", name, key)
			}
		}
		if svc.Resources != nil {
			effective := effectiveServiceResources(svc.Resources)
			for _, field := range []struct{ name, request, limit string }{
				{"cpu", effective.Requests.CPU, effective.Limits.CPU},
				{"memory", effective.Requests.Memory, effective.Limits.Memory},
			} {
				request, limit := resource.Quantity{}, resource.Quantity{}
				for _, entry := range []struct {
					name, value string
					target      *resource.Quantity
				}{
					{"requests", field.request, &request}, {"limits", field.limit, &limit},
				} {
					if entry.value == "" {
						continue
					}
					q, err := resource.ParseQuantity(entry.value)
					if err != nil || q.Sign() < 0 {
						return fmt.Errorf("service %q: resources.%s.%s must be a non-negative Kubernetes quantity", name, entry.name, field.name)
					}
					*entry.target = q
				}
				if field.limit != "" && request.Cmp(limit) > 0 {
					return fmt.Errorf("service %q: resources.requests.%s exceeds its limit", name, field.name)
				}
			}
		}
		if err := validateVolumes(name, svc.Volumes); err != nil {
			return err
		}
	}
	return nil
}

func validateVolumes(service string, volumes []Volume) error {
	names, paths := map[string]bool{}, map[string]bool{}
	for _, volume := range volumes {
		if errs := validation.IsDNS1123Label(volume.Name); len(errs) > 0 || volume.Name == "tmp" {
			return fmt.Errorf("service %q: volume %q needs a DNS label other than the reserved name tmp", service, volume.Name)
		}
		if names[volume.Name] || paths[volume.Path] {
			return fmt.Errorf("service %q: duplicate volume name or mount path %q", service, volume.Name)
		}
		names[volume.Name], paths[volume.Path] = true, true
		if !path.IsAbs(volume.Path) || path.Clean(volume.Path) != volume.Path || volume.Path == "/" || volume.Path == "/tmp" {
			return fmt.Errorf("service %q: volume %q needs an absolute mount path other than / or /tmp", service, volume.Name)
		}
		if strings.IndexFunc(volume.Path, unicode.IsControl) >= 0 || strings.ContainsAny(volume.Path, "\u2028\u2029") {
			return fmt.Errorf("service %q: volume %q path must not contain control characters or line breaks", service, volume.Name)
		}
		if volume.Claim != "" {
			if errs := validation.IsDNS1123Subdomain(volume.Claim); len(errs) > 0 {
				return fmt.Errorf("service %q: volume %q claim must be a PVC name", service, volume.Name)
			}
		}
		if volume.Size != "" {
			size, err := resource.ParseQuantity(volume.Size)
			if err != nil || size.Sign() <= 0 {
				return fmt.Errorf("service %q: volume %q needs a positive storage quantity", service, volume.Name)
			}
		}
	}
	return nil
}

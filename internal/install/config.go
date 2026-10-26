package install

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Config contains only non-secret installation choices. It is persisted in the
// dedicated GitOps tree, separately from application manifests and Helmfiles.
type Config struct {
	Cluster        string `yaml:"cluster"`
	Namespace      string `yaml:"namespace"`
	BuildNamespace string `yaml:"buildNamespace"`
	GitProvider    string `yaml:"gitProvider"`
	GitURL         string `yaml:"gitURL"`
	GitUser        string `yaml:"gitUser"`
	GitOpsURL      string `yaml:"gitopsURL"`
	GitOpsBranch   string `yaml:"gitopsBranch"`
	// AppsURL is the repository the control plane may write: project
	// manifests, values and previews. It is separate from the platform
	// repository so that a compromised control plane cannot rewrite the
	// chart or the Flux configuration that grants cluster rights. Empty
	// keeps the historical single-repository layout.
	AppsURL            string            `yaml:"appsURL,omitempty"`
	AppsBranch         string            `yaml:"appsBranch,omitempty"`
	AppsRoot           string            `yaml:"appsRoot,omitempty"`
	Root               string            `yaml:"root"`
	Source             string            `yaml:"source"`
	RegistryURL        string            `yaml:"registryURL"`
	RegistryUser       string            `yaml:"registryUser"`
	Host               string            `yaml:"host"`
	Ingress            bool              `yaml:"ingress"`
	IngressClass       string            `yaml:"ingressClass"`
	IngressNamespace   string            `yaml:"ingressNamespace"`
	IngressPodSelector map[string]string `yaml:"ingressPodSelector,omitempty"`
	TLS                bool              `yaml:"tls"`
	Issuer             string            `yaml:"issuer"`
	ACMEEmail          string            `yaml:"acmeEmail,omitempty"`
	StorageClass       string            `yaml:"storageClass"`
	Image              string            `yaml:"image"`
	BuilderImage       string            `yaml:"builderImage"`
	BuilderMemory      string            `yaml:"builderMemory"`
	BuildkitMemory     string            `yaml:"buildkitMemory"`
	BuildkitImage      string            `yaml:"buildkitImage,omitempty"`
	// BuilderRuntimeClass donne une sandbox aux Pods de build (gVisor,
	// Kata). Vide conserve le runtime par défaut et le noyau partagé.
	BuilderRuntimeClass string `yaml:"builderRuntimeClass,omitempty"`
	// BuilderNodeSelector est un objet JSON de labels : il place les
	// builds sur un pool de nœuds dédié. Vide garde le défaut historique.
	BuilderNodeSelector string `yaml:"builderNodeSelector,omitempty"`
	// BuilderTolerations est un tableau JSON de tolérances qui permet aux
	// builds d'occuper le pool tainté. Vide garde le défaut historique.
	BuilderTolerations    string `yaml:"builderTolerations,omitempty"`
	NodePort              int    `yaml:"nodePort,omitempty"`
	WebhookURL            string `yaml:"webhookURL,omitempty"`
	PrivateBuildEgress    bool   `yaml:"privateBuildEgress,omitempty"`
	ClusterServices       bool   `yaml:"clusterServices"`
	RequireProjectSecrets bool   `yaml:"requireProjectSecrets,omitempty"`
	Infisical             bool   `yaml:"infisical"`
	InfisicalHost         string `yaml:"infisicalHost,omitempty"`
	AdminEmail            string `yaml:"adminEmail,omitempty"`
	CAFile                string `yaml:"-"`
	Timeout               string `yaml:"timeout"`
}

// ProjectsPath is where project manifests live in the applications
// repository. The platform repository no longer contains projects when the
// two-repository layout is used.
func (c Config) ProjectsPath() string         { return path.Join(c.AppsRoot, "projects") }
func (c Config) PlatformPath() string         { return path.Join(c.Root, "platform") }
func (c Config) ChartPath(name string) string { return path.Join(c.Root, "charts", name) }

// SplitApps reports whether the applications live in their own repository.
func (c Config) SplitApps() bool {
	return c.AppsURL != "" && c.AppsURL != c.GitOpsURL
}

func validRelativeRoot(root string) bool {
	if root == "" {
		return true
	}
	if path.Clean(root) != root || strings.HasPrefix(root, "/") || root == "." || root == ".." || strings.HasPrefix(root, "../") || strings.Contains(root, "\\") {
		return false
	}
	for _, segment := range strings.Split(root, "/") {
		if segment == ".git" {
			return false
		}
	}
	return true
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil
}

func (c Config) Validate() error {
	for _, item := range []struct{ name, value string }{{"cluster", c.Cluster}, {"namespace", c.Namespace}, {"build namespace", c.BuildNamespace}, {"source", c.Source}} {
		if errs := validation.IsDNS1123Label(item.value); len(errs) > 0 {
			return fmt.Errorf("invalid %s %q: %s", item.name, item.value, strings.Join(errs, ", "))
		}
	}
	if c.Namespace == c.BuildNamespace {
		return fmt.Errorf("control plane and build namespaces must differ")
	}
	if !validRelativeRoot(c.Root) || c.Root == "" {
		return fmt.Errorf("root must be a dedicated relative GitOps directory without traversal")
	}
	if !validHTTPURL(c.GitOpsURL) {
		return fmt.Errorf("gitops URL must be HTTP(S), without embedded credentials")
	}
	if c.AppsURL == "" {
		return fmt.Errorf("an applications GitOps repository is required (--gitops-apps-url); the single-repository layout is no longer supported")
	}
	if !validHTTPURL(c.AppsURL) {
		return fmt.Errorf("applications GitOps URL must be HTTP(S), without embedded credentials")
	}
	if c.AppsURL == c.GitOpsURL {
		return fmt.Errorf("the applications repository must differ from the platform repository")
	}
	if c.AppsBranch == "" || strings.HasPrefix(c.AppsBranch, "-") {
		return fmt.Errorf("applications GitOps branch is required")
	}
	if !validRelativeRoot(c.AppsRoot) {
		return fmt.Errorf("applications root must be a relative GitOps directory without traversal")
	}
	if c.GitOpsBranch == "" || strings.HasPrefix(c.GitOpsBranch, "-") {
		return fmt.Errorf("gitops branch is required")
	}
	if c.Ingress {
		if errs := validation.IsDNS1123Subdomain(c.Host); len(errs) > 0 {
			return fmt.Errorf("invalid platform hostname %q", c.Host)
		}
	}
	if c.TLS && !c.Ingress {
		return fmt.Errorf("tls requires ingress")
	}
	if c.TLS && c.Issuer == "" {
		return fmt.Errorf("tls requires a certificate issuer")
	}
	if c.Infisical && !c.Ingress {
		return fmt.Errorf("infisical needs a hostname and ingress for developer access")
	}
	if c.NodePort != 0 && (c.NodePort < 30000 || c.NodePort > 32767) {
		return fmt.Errorf("node port must be between 30000 and 32767")
	}
	if c.Image == "" || c.BuilderImage == "" {
		return fmt.Errorf("control plane and builder images are required")
	}
	for _, quantity := range []string{c.BuilderMemory, c.BuildkitMemory} {
		if quantity != "" {
			q, err := resource.ParseQuantity(quantity)
			if err != nil || q.Sign() <= 0 {
				return fmt.Errorf("invalid builder memory quantity %q", quantity)
			}
		}
	}
	// Le placement des builds est optionnel, mais une valeur fournie est
	// validée avant d'écrire le GitOps : un nom de RuntimeClass ou un label
	// faux ne doit pas être découvert au premier build.
	if err := builder.ValidateRuntimeClass(c.BuilderRuntimeClass); err != nil {
		return fmt.Errorf("builder runtime class: %w", err)
	}
	if _, err := builder.ParseNodeSelector(c.BuilderNodeSelector); err != nil {
		return fmt.Errorf("builder node selector: %w", err)
	}
	if _, err := builder.ParseTolerations(c.BuilderTolerations); err != nil {
		return fmt.Errorf("builder tolerations: %w", err)
	}
	if c.GitProvider != "github" && c.GitProvider != "gitlab" && c.GitProvider != "forgejo" {
		return fmt.Errorf("unknown git provider %q", c.GitProvider)
	}
	return nil
}

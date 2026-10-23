package install

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"net/url"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Config contains only non-secret installation choices. It is persisted in the
// dedicated GitOps tree, separately from application manifests and Helmfiles.
type Config struct {
	Cluster            string            `yaml:"cluster"`
	Namespace          string            `yaml:"namespace"`
	BuildNamespace     string            `yaml:"buildNamespace"`
	GitProvider        string            `yaml:"gitProvider"`
	GitURL             string            `yaml:"gitURL"`
	GitUser            string            `yaml:"gitUser"`
	GitOpsURL          string            `yaml:"gitopsURL"`
	GitOpsBranch       string            `yaml:"gitopsBranch"`
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
	NodePort           int               `yaml:"nodePort,omitempty"`
	WebhookURL         string            `yaml:"webhookURL,omitempty"`
	PrivateBuildEgress bool              `yaml:"privateBuildEgress,omitempty"`
	ClusterServices    bool              `yaml:"clusterServices"`
	Infisical          bool              `yaml:"infisical"`
	InfisicalHost      string            `yaml:"infisicalHost,omitempty"`
	AdminEmail         string            `yaml:"adminEmail,omitempty"`
	CAFile             string            `yaml:"-"`
	Timeout            string            `yaml:"timeout"`
}

func (c Config) ProjectsPath() string         { return path.Join(c.Root, "projects") }
func (c Config) PlatformPath() string         { return path.Join(c.Root, "platform") }
func (c Config) ChartPath(name string) string { return path.Join(c.Root, "charts", name) }

func (c Config) Validate() error {
	for _, item := range []struct{ name, value string }{{"cluster", c.Cluster}, {"namespace", c.Namespace}, {"build namespace", c.BuildNamespace}, {"source", c.Source}} {
		if errs := validation.IsDNS1123Label(item.value); len(errs) > 0 {
			return fmt.Errorf("invalid %s %q: %s", item.name, item.value, strings.Join(errs, ", "))
		}
	}
	if c.Namespace == c.BuildNamespace {
		return fmt.Errorf("control plane and build namespaces must differ")
	}
	if c.Root == "" || path.Clean(c.Root) != c.Root || strings.HasPrefix(c.Root, "/") || c.Root == "." || c.Root == ".." || strings.HasPrefix(c.Root, "../") || strings.Contains(c.Root, "\\") {
		return fmt.Errorf("root must be a dedicated relative GitOps directory without traversal")
	}
	for _, segment := range strings.Split(c.Root, "/") {
		if segment == ".git" {
			return fmt.Errorf("GitOps root cannot contain .git")
		}
	}
	u, err := url.Parse(c.GitOpsURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("gitops URL must be HTTP(S), without embedded credentials")
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
		return fmt.Errorf("TLS requires ingress")
	}
	if c.TLS && c.Issuer == "" {
		return fmt.Errorf("TLS requires a certificate issuer")
	}
	if c.Infisical && !c.Ingress {
		return fmt.Errorf("Infisical needs a hostname and ingress for developer access")
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
	if c.GitProvider != "github" && c.GitProvider != "gitlab" && c.GitProvider != "forgejo" {
		return fmt.Errorf("unknown git provider %q", c.GitProvider)
	}
	return nil
}

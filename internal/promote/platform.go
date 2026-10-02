package promote

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// Platform is the cluster-shaped GitOps layout. App spec stays small;
// these fields are the defaults Kuberpack expands into Helm values.
type Platform struct {
	AppsDir             string
	PreviewsDir         string
	PreviewsBranch      string
	PreviewDomain       string
	ChartRef            string
	ChartSourceKind     string
	SourceName          string
	SourceNamespace     string
	ReleaseNamespace    string
	IngressClassName    string
	ImagePullSecret     string
	IngressTLS          bool
	ExternalDNSTarget   string
	ExternalDNSTTL      string
	TraefikEntrypoint   string
	TraefikCertResolver string
	NetworkPolicy       bool
	// Infisical: where app secrets are synced from. Empty ProjectID disables
	// the generated InfisicalStaticSecret.
	InfisicalAuthRef   string
	InfisicalProjectID string
	InfisicalPath      string
	InfisicalDir       string
	InfisicalNamespace string
}

func (p Platform) withDefaults() Platform {
	if p.AppsDir == "" {
		p.AppsDir = "kubernetes/vps/apps"
	}
	if p.PreviewsDir == "" {
		p.PreviewsDir = "kubernetes/vps/previews"
	}
	if p.PreviewsBranch == "" {
		p.PreviewsBranch = "previews"
	}
	if p.ChartRef == "" {
		p.ChartRef = "./kubernetes/vps/charts/stateless"
	}
	if p.ChartSourceKind == "" {
		p.ChartSourceKind = "GitRepository"
	}
	if p.SourceName == "" {
		p.SourceName = "infra-homelab"
	}
	if p.SourceNamespace == "" {
		p.SourceNamespace = "flux-system"
	}
	if p.ReleaseNamespace == "" {
		p.ReleaseNamespace = "apps"
	}
	if p.IngressClassName == "" {
		p.IngressClassName = "traefik"
	}
	if p.ImagePullSecret == "" {
		p.ImagePullSecret = "forgejo-registry-pull"
	}
	p.ExternalDNSTarget = IPv6DNSTargets(p.ExternalDNSTarget)
	if p.ExternalDNSTTL == "" {
		p.ExternalDNSTTL = "300"
	}
	if p.TraefikEntrypoint == "" {
		p.TraefikEntrypoint = "websecure"
	}
	if p.TraefikCertResolver == "" {
		p.TraefikCertResolver = "letsencrypt"
	}
	if p.InfisicalAuthRef == "" {
		p.InfisicalAuthRef = "contabo-vps"
	}
	if p.InfisicalPath == "" {
		p.InfisicalPath = "/"
	}
	if p.InfisicalDir == "" {
		p.InfisicalDir = "kubernetes/vps/infisical"
	}
	if p.InfisicalNamespace == "" {
		p.InfisicalNamespace = "infisical"
	}
	return p
}

// InfisicalEnabled reports whether the platform is configured to generate
// InfisicalStaticSecret objects for apps that declare secrets.
func (p Platform) InfisicalEnabled() bool {
	return strings.TrimSpace(p.withDefaults().InfisicalProjectID) != ""
}

func (p Platform) InfisicalCRPath(name string) string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().InfisicalDir, "app-"+sanitizeName(name)+".yaml"))
}

func (p Platform) InfisicalParentPath() string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().InfisicalDir, "kustomization.yaml"))
}

func (p Platform) ParentPath() string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().AppsDir, "kustomization.yaml"))
}

func (p Platform) AppDir(name string) string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().AppsDir, name))
}

func (p Platform) ValuesPath(name string) string {
	return filepath.ToSlash(filepath.Join(p.AppDir(name), "values.yaml"))
}

func (p Platform) PreviewParentPath() string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().PreviewsDir, "kustomization.yaml"))
}

func (p Platform) PreviewDir(name string) string {
	return filepath.ToSlash(filepath.Join(p.withDefaults().PreviewsDir, name))
}

func (p Platform) PreviewValuesPath(name string) string {
	return filepath.ToSlash(filepath.Join(p.PreviewDir(name), "values.yaml"))
}

func PreviewReleaseName(app string, pr int) string {
	return app + "-pr-" + strconv.Itoa(pr)
}

func PreviewHostname(app string, pr int, domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	return "pr-" + strconv.Itoa(pr) + "." + app + "." + strings.TrimPrefix(domain, ".")
}

// IPv6DNSTargets keeps only IPv6 addresses so ExternalDNS publishes AAAA, never A.
func IPv6DNSTargets(raw string) string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip := net.ParseIP(part)
		if ip == nil || ip.To4() != nil {
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, ",")
}

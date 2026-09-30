package promote

import "path/filepath"

// Platform is the cluster-shaped GitOps layout. App spec stays small;
// these fields are the defaults Kuberpack expands into Helm values.
type Platform struct {
	AppsDir             string
	ChartRef            string
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
}

func (p Platform) withDefaults() Platform {
	if p.AppsDir == "" {
		p.AppsDir = "kubernetes/vps/apps"
	}
	if p.ChartRef == "" {
		p.ChartRef = "./kubernetes/vps/charts/stateless"
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
	if p.ExternalDNSTTL == "" {
		p.ExternalDNSTTL = "300"
	}
	if p.TraefikEntrypoint == "" {
		p.TraefikEntrypoint = "websecure"
	}
	if p.TraefikCertResolver == "" {
		p.TraefikCertResolver = "letsencrypt"
	}
	return p
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

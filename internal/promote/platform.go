package promote

import (
	"fmt"
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
	// Issuer is the cert-manager issuer that signs the certificates of the
	// applications. Empty means the platform has no issuer.
	Issuer string
	// ControlPlaneNamespace is the namespace of the control plane. The
	// application namespaces grant it the right to write the secret that
	// pulls the images of the registry.
	ControlPlaneNamespace string
	NetworkPolicy         bool
	// Infisical: where app secrets are synced from. Empty ProjectID disables
	// the generated InfisicalStaticSecret.
	InfisicalAuthRef   string
	InfisicalProjectID string
	InfisicalPath      string
	InfisicalDir       string
	InfisicalNamespace string
	// PostgresImage is the image of the PostgreSQL databases of the
	// projects. It is a digest: an addon is pinned like an application.
	PostgresImage string
	// AddonOperators are the operators the cluster has. A type of addon
	// that writes the resources of an operator is refused when the
	// operator is not declared: the project would fail to apply as a
	// whole, not only the database.
	AddonOperators []string
	// NodeMemory is the memory of the node. The compiler refuses a
	// project that does not fit: an application that is scheduled and
	// then killed takes the whole cluster with it.
	NodeMemory string
	// NodeCPU is the cpu of the node, in cores.
	NodeCPU float64
	// NodeReserve is the share of the node the platform keeps for
	// itself, in percent. The applications of the platform share what is
	// left: the control plane, the ingress, the operators and the
	// databases of the platform have to run too, and an application that
	// takes the node takes the API of the platform with it.
	NodeReserve string
	// PreviewDatabase* configure the per-preview CloudNativePG database
	// (empty, isolated). Used only when an app opts in.
	PreviewDatabaseStorage string
	PreviewDatabaseClass   string
	// ProjectsDir is where project manifests and their compiled artifacts live.
	ProjectsDir string
	// AddonImages holds the image of each addon engine, by engine name.
	AddonImages map[string]string
	// PostgresStorage is the size of the volume of the PostgreSQL
	// databases of the projects.
	PostgresStorage string
	// PostgresStorageClass is the storage class of the volumes of the
	// PostgreSQL databases of the projects.
	PostgresStorageClass string
	// AddonStorage holds the volume size of each addon engine.
	AddonStorage map[string]string
	// AddonStorageClass is the storage class of the addon volumes.
	AddonStorageClass string
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
	if p.Issuer == "" {
		p.Issuer = "letsencrypt"
	}
	if p.ControlPlaneNamespace == "" {
		p.ControlPlaneNamespace = "kuberpack"
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
	if p.PostgresStorage == "" {
		p.PostgresStorage = "1Gi"
	}
	if p.PostgresStorageClass == "" {
		p.PostgresStorageClass = "local-path"
	}
	if p.PreviewDatabaseStorage == "" {
		p.PreviewDatabaseStorage = "1Gi"
	}
	if p.PreviewDatabaseClass == "" {
		p.PreviewDatabaseClass = "local-path"
	}
	if p.ProjectsDir == "" {
		p.ProjectsDir = "kubernetes/vps/projects"
	}
	if p.AddonImages == nil {
		p.AddonImages = map[string]string{}
	}
	if _, ok := p.AddonImages["valkey"]; !ok {
		p.AddonImages["valkey"] = "docker.io/valkey/valkey@sha256:081c2f5cb575efc901aa80ff9cdbd1ec6a301682fd35e1ebb4b0990a4a4a8507"
	}
	if p.AddonStorage == nil {
		p.AddonStorage = map[string]string{}
	}
	if _, ok := p.AddonStorage["valkey"]; !ok {
		p.AddonStorage["valkey"] = "1Gi"
	}
	if p.AddonStorageClass == "" {
		p.AddonStorageClass = p.PreviewDatabaseClass
	}
	return p
}

// InfisicalEnabled reports whether the platform is configured to generate
// InfisicalStaticSecret objects for apps that declare secrets.
func (p Platform) InfisicalEnabled() bool {
	return strings.TrimSpace(p.withDefaults().InfisicalProjectID) != ""
}

func (p Platform) InfisicalCRPath(name string) string {
	// The name is already the name of the object, prefix included: the
	// file must carry it tel quel, sans un second prefix.
	return filepath.ToSlash(filepath.Join(p.withDefaults().InfisicalDir, sanitizeName(name)+".yaml"))
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

// PinnedImage checks that an image is identified by its digest. A tag can be
// moved: the addon would then run something else than what was reviewed.
func (p Platform) PinnedImage(image string) (string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", fmt.Errorf("the image of the addon is empty")
	}
	if !strings.Contains(image, "@sha256:") {
		return "", fmt.Errorf("the image %q of the addon is not pinned: use a digest", image)
	}
	return image, nil
}

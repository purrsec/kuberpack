package promote

import (
	"context"

	"fmt"
	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AddonPeer is a network peer that is an addon of the project: the
// NetworkPolicy lets the application reach it.
type AddonPeer struct {
	// Kind is the kind of the addon (postgres, valkey).
	Kind string
	// Name is the name of the addon in the manifest. Without it, two
	// databases of the same project are indistinguishable, and a service
	// that declares one reaches the other.
	Name string
	// For is the project the addon belongs to.
	For string
	// Port is the port the addon listens on.
	Port int
}

// EnvVar is an environment variable of the application: a literal value, or
// a reference to a Kubernetes Secret. An addon provides the second kind.
type EnvVar struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	// SecretName and SecretKey reference a value in a Secret. The value
	// never transits through Kuberpack.
	SecretName string `yaml:"secret_name,omitempty" json:"secret_name,omitempty"`
	SecretKey  string `yaml:"secret_key,omitempty" json:"secret_key,omitempty"`
}

// AppSpec is what you describe. Kuberpack expands it to Helm + Kustomize.
type AppSpec struct {
	Name        string
	Hostname    string
	Port        int
	Healthcheck string
	Replicas    *int
	Preview     bool
	Internet    *bool
	Peers       []string
	// Env are the literal environment variables of the application.
	Env []EnvVar
	// Command is what the container runs, when the image expects
	// something else than its default.
	Command []string
	// AddonPeers are the addons of the project the application may reach.
	AddonPeers []AddonPeer
	// NetworkPolicy overrides the platform default for the NetworkPolicy of
	// the service. A manifest with defaultDeny: false declares an app that
	// is not isolated.
	NetworkPolicy *bool
	// Project is the manifest project the service belongs to (empty outside
	// a project). It scopes the generated Infisical object so two projects
	// can declare a service with the same name.
	Project string
	// Namespace overrides the platform release namespace. A project with its
	// own namespace keeps its services, its addons and its secrets there.
	Namespace string
	// Secrets are Infisical key names to inject into the app Secret as env
	// vars of the same name. SecretEnv is the Infisical environment slug.
	Secrets   []string
	SecretEnv string
	// InheritSecretFrom drives which Secret a preview inherits in addition to
	// its exact app-<app>-pr-<n>: "prod" mounts the production Secret
	// app-<app>, "dev" mounts the shared `dev` Secret (Infisical `dev` env).
	InheritSecretFrom string
	// PreviewDatabase provisions an empty, isolated CloudNativePG database for
	// the preview and points DATABASE_URL at it (opt-in).
	PreviewDatabase bool
	// TLSSecretName is the secret that holds the certificate of the
	// application. It is empty when the platform has no issuer.
	TLSSecretName string
	// Image is the published image the application runs, when it is not
	// built from a repository.
	Image string
	// Volumes are the volumes of the application.
	Volumes []Volume
	// WritableRoot lets the application write in its own filesystem.
	WritableRoot bool
	// RunAsRoot lets the application run as root.
	RunAsRoot bool
	// Capabilities are the capabilities the application needs.
	Capabilities []string
	// Resources are the cpu and the memory of the application.
	Resources *ServiceResources
	// StartupBudget is how long the application may take to start, in
	// seconds.
	StartupBudget int
}

// PreviewSecretSource values for AppSpec.InheritSecretFrom.
const (
	SecretFromProd = "prod"
	SecretFromDev  = "dev"
)

// inheritedPreviewSecretName is the extra Secret a preview mounts beyond its
// exact app-<app>-pr-<n>. Production secrets are only used when explicitly
// requested with inherit_secret_from: prod.
func inheritedPreviewSecretName(spec AppSpec) string {
	if strings.EqualFold(strings.TrimSpace(spec.InheritSecretFrom), SecretFromProd) {
		return AppRuntimeSecretName(previewBaseName(spec.Name))
	}
	return DevSecretName
}

// previewBaseName strips the `-pr-<n>` suffix to recover the production app
// name from a preview release name (ecms-pr-1 -> ecms).
func previewBaseName(name string) string {
	name = sanitizeName(name)
	if i := strings.LastIndex(name, "-pr-"); i > 0 {
		if _, err := strconv.Atoi(name[i+4:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// EnsureRequest creates GitOps files for a new app. It does not enable Flux
// (parent kustomization) until promote writes a digest-pinned image.
type EnsureRequest struct {
	GitOpsURL    string
	GitOpsBranch string
	HTTPToken    string
	// AuthArgs are the git arguments that authenticate the clone of the
	// GitOps repository. Every forge has its own way.
	AuthArgs []string
	// AuthScheme is how the forge of the GitOps repository authenticates a
	// clone: "token" for a Gitea, "basic" for GitHub and GitLab.
	AuthScheme  string
	MaxAttempts int
	App         AppSpec
	Platform    Platform
}

// authArgs is how the clone of the GitOps repository is authenticated.
func (req EnsureRequest) authArgs() []string {
	if req.AuthArgs != nil {
		return req.AuthArgs
	}
	return fetch.AuthArgs(req.AuthScheme, req.HTTPToken)
}

func (req EnsureRequest) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(req.authArgs(), args...)...)
}

// EnsureApp writes values, HelmRelease and app kustomization if they are missing.
func normalizeSpec(spec AppSpec) (AppSpec, error) {
	spec.Name = sanitizeName(spec.Name)
	if spec.Name == "" {
		return AppSpec{}, fmt.Errorf("app name is required")
	}
	spec.Hostname = strings.TrimSpace(spec.Hostname)
	if spec.Hostname == "" {
		return AppSpec{}, fmt.Errorf("hostname is required")
	}
	if spec.Port == 0 {
		spec.Port = 8080
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return AppSpec{}, fmt.Errorf("port is invalid")
	}
	spec.Healthcheck = strings.TrimSpace(spec.Healthcheck)
	if spec.Healthcheck == "" {
		spec.Healthcheck = "/"
	}
	if !strings.HasPrefix(spec.Healthcheck, "/") {
		spec.Healthcheck = "/" + spec.Healthcheck
	}
	if spec.Replicas == nil {
		one := 1
		spec.Replicas = &one
	}
	if spec.Preview {
		spec.Peers = nil
		// An isolated, empty database per preview peers only to that database.
		if spec.PreviewDatabase {
			spec.Peers = []string{"postgres:" + sanitizeName(spec.Name)}
		}
	}
	peers, err := ParsePeers(spec.Peers)
	if err != nil {
		return AppSpec{}, err
	}
	spec.Peers = FormatPeers(peers)
	return spec, nil
}

func writeMissingAppFiles(work string, spec AppSpec, p Platform) (bool, error) {
	p = p.withDefaults()
	return writeMissingAppFilesIn(work, p.AppDir(spec.Name), spec, p)
}

// writeMissingAppFilesIn writes the GitOps files of a service in a given
// directory. The directory of a service of a project is not the app
// directory: the manifest owns the layout.
func writeMissingAppFilesIn(work string, serviceDir string, spec AppSpec, p Platform) (bool, error) {
	p = p.withDefaults()
	dir := filepath.Join(work, filepath.FromSlash(serviceDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	changed := false
	files := map[string]string{
		"values.yaml":        valuesYAML(spec, p),
		"helmrelease.yaml":   helmReleaseYAML(spec, p),
		"kustomization.yaml": appKustomizationYAML(spec, p),
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		if err == nil {
			continue
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// servicePaths returns the directory of a service, the kustomization of its
// parent and the name of the resource the parent must list.
//
// A standalone app lives in <apps>/<app>/values.yaml: its parent is the apps
// kustomization. A service of a project lives in
// <projects>/<project>/<service>/values.yaml: its parent is the kustomization
// of the project. A preview lives in <previews>/<app>/values.yaml.
func servicePaths(valuesPath string) (dir string, parent string, resource string) {
	dir = filepath.ToSlash(filepath.Dir(valuesPath))
	parent = filepath.ToSlash(filepath.Join(filepath.Dir(dir), "kustomization.yaml"))
	resource = filepath.Base(dir)
	return dir, parent, resource
}

func enableInParent(work string, name string, p Platform) (bool, error) {
	p = p.withDefaults()
	return enableInParentAt(work, p.ParentPath(), name)
}

// enableInParentAt lists a resource in a given parent kustomization.
func enableInParentAt(work string, parentPath string, name string) (bool, error) {
	path := filepath.Join(work, filepath.FromSlash(parentPath))
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		raw = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, fmt.Errorf("read %s: %w", parentPath, err)
	}
	if listedInKustomization(raw, name) {
		return false, nil
	}
	if err := os.WriteFile(path, appendKustomizationResource(raw, name), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func valuesYAML(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	var b strings.Builder
	b.WriteString("# Desired runtime. Kuberpack rewrites `image` and `networkPolicy`.\n")
	b.WriteString("track: main\n")
	if spec.Image != "" {
		b.WriteString("image: " + strconv.Quote(spec.Image) + "\n")
	} else {
		b.WriteString("image: \"\"\n")
	}
	b.WriteString("port: " + strconv.Itoa(spec.Port) + "\n")
	b.WriteString("healthcheck: " + spec.Healthcheck + "\n")
	b.WriteString("hostname: " + spec.Hostname + "\n")
	b.WriteString("replicas: " + strconv.Itoa(replicaCount(spec)) + "\n")
	b.WriteString("ingressClassName: " + p.IngressClassName + "\n")
	if p.ImagePullSecret != "" {
		b.WriteString("imagePullSecrets:\n")
		b.WriteString("  - name: " + p.ImagePullSecret + "\n")
	}
	b.WriteString("ingress:\n")
	if p.IngressTLS {
		b.WriteString("  tls: true\n")
		if spec.TLSSecretName != "" {
			b.WriteString("  tlsSecretName: " + spec.TLSSecretName + "\n")
		}
	} else {
		b.WriteString("  tls: false\n")
	}
	b.WriteString("  annotations:\n")
	b.WriteString("    external-dns.alpha.kubernetes.io/hostname: " + spec.Hostname + "\n")
	if p.ExternalDNSTarget != "" {
		b.WriteString("    external-dns.alpha.kubernetes.io/target: " + strconv.Quote(p.ExternalDNSTarget) + "\n")
	}
	if p.ExternalDNSTTL != "" {
		b.WriteString("    external-dns.alpha.kubernetes.io/ttl: " + strconv.Quote(p.ExternalDNSTTL) + "\n")
	}
	if p.TraefikEntrypoint != "" {
		b.WriteString("    traefik.ingress.kubernetes.io/router.entrypoints: " + p.TraefikEntrypoint + "\n")
	}
	// The certificate resolver belongs to the TLS configuration: Traefik
	// refuses to create a router that points to a resolver it does not know,
	// and an ingress without TLS has no certificate to resolve.
	// The resolver of Traefik is only used when the platform does not issue
	// the certificates itself: a router that names a resolver that does
	// not exist is refused.
	if p.IngressTLS && p.TraefikCertResolver != "" && spec.TLSSecretName == "" {
		b.WriteString("    traefik.ingress.kubernetes.io/router.tls.certresolver: " + p.TraefikCertResolver + "\n")
	}
	// The volumes of the application: a temporary one, or a disk that
	// outlives the pod.
	if len(spec.Volumes) > 0 {
		b.WriteString("volumes:\n")
		for _, volume := range spec.Volumes {
			b.WriteString("  - name: " + volume.Name + "\n")
			b.WriteString("    path: " + volume.Path + "\n")
			if volume.Size != "" {
				b.WriteString("    size: " + volume.Size + "\n")
			}
			if volume.Claim != "" {
				b.WriteString("    claim: " + volume.Claim + "\n")
			}
		}
	}
	if spec.StartupBudget > 0 {
		b.WriteString("startupProbe:\n")
		b.WriteString("  periodSeconds: 2\n")
		b.WriteString("  failureThreshold: " + strconv.Itoa(spec.StartupBudget/2) + "\n")
	}
	if spec.WritableRoot {
		b.WriteString("writableRoot: true\n")
	}
	if spec.RunAsRoot {
		b.WriteString("runAsRoot: true\n")
	}
	if len(spec.Capabilities) > 0 {
		b.WriteString("capabilities:\n")
		for _, capability := range spec.Capabilities {
			b.WriteString("  - " + capability + "\n")
		}
	}
	b.WriteString("networkPolicy:\n")
	if networkPolicyEnabled(spec, p) {
		b.WriteString("  enabled: true\n")
	} else {
		b.WriteString("  enabled: false\n")
	}
	if internetEnabled(spec) {
		b.WriteString("  internet: true\n")
	} else {
		b.WriteString("  internet: false\n")
	}
	if len(spec.Peers) == 0 {
		b.WriteString("  peers: []\n")
	} else {
		b.WriteString("  peers:\n")
		for _, peer := range spec.Peers {
			b.WriteString("    - " + peer + "\n")
		}
	}
	if len(spec.AddonPeers) == 0 {
		b.WriteString("  addonPeers: []\n")
	} else {
		b.WriteString("  addonPeers:\n")
		for _, peer := range spec.AddonPeers {
			b.WriteString("    - kind: " + peer.Kind + "\n")
			if peer.Name != "" {
				b.WriteString("      name: " + peer.Name + "\n")
			}
			b.WriteString("      for: " + peer.For + "\n")
			b.WriteString("      port: " + strconv.Itoa(peer.Port) + "\n")
		}
	}
	// The size of the service: the manifest decides, the platform's
	// defaults apply when it does not say.
	if spec.Resources != nil {
		b.WriteString("resources:\n")
		if spec.Resources.Requests.CPU != "" || spec.Resources.Requests.Memory != "" {
			b.WriteString("  requests:\n")
			if spec.Resources.Requests.CPU != "" {
				b.WriteString("    cpu: " + spec.Resources.Requests.CPU + "\n")
			}
			if spec.Resources.Requests.Memory != "" {
				b.WriteString("    memory: " + spec.Resources.Requests.Memory + "\n")
			}
		}
		if spec.Resources.Limits.CPU != "" || spec.Resources.Limits.Memory != "" {
			b.WriteString("  limits:\n")
			if spec.Resources.Limits.CPU != "" {
				b.WriteString("    cpu: \"" + spec.Resources.Limits.CPU + "\"\n")
			}
			if spec.Resources.Limits.Memory != "" {
				b.WriteString("    memory: \"" + spec.Resources.Limits.Memory + "\"\n")
			}
		}
	}
	if spec.Preview && spec.Resources == nil {
		b.WriteString("resources:\n")
		b.WriteString("  requests:\n")
		b.WriteString("    cpu: 25m\n")
		b.WriteString("    memory: 32Mi\n")
		b.WriteString("  limits:\n")
		b.WriteString("    cpu: 200m\n")
		b.WriteString("    memory: 128Mi\n")
	}
	// The environment of the service: its literal variables, then the URL of
	// the preview's own database (highest priority, pulled from the
	// CNPG-generated Secret).
	if env := envYAML(spec); env != "" {
		b.WriteString("env:\n")
		for _, line := range strings.Split(strings.TrimRight(env, "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	// The command the container runs, when the image expects one.
	if len(spec.Command) > 0 {
		b.WriteString("command:\n")
		for _, part := range spec.Command {
			b.WriteString("  - " + strconv.Quote(part) + "\n")
		}
	}
	if spec.Preview {
		// A preview reads its own exact Secret (app-<app>-pr-<n>) and, in
		// addition, inherits one source chosen by inherit_secret_from:
		// "prod" -> the production Secret, "dev" (default) -> the shared `dev`
		// Secret synced from the Infisical `dev` environment. Both optional.
		b.WriteString("envFrom:\n")
		b.WriteString("  - secretRef:\n")
		b.WriteString("      name: " + AppRuntimeSecretName(spec.Name) + "\n")
		b.WriteString("      optional: true\n")
		b.WriteString("  - secretRef:\n")
		b.WriteString("      name: " + inheritedPreviewSecretName(spec) + "\n")
		b.WriteString("      optional: true\n")
	} else {
		// The Secret is created by an administrator, not by Kuberpack. Mark it
		// optional so a secretless app (a frontend, a first deploy) still
		// starts; present keys are injected normally.
		b.WriteString("envFrom:\n")
		b.WriteString("  - secretRef:\n")
		b.WriteString("      name: " + AppRuntimeSecretName(spec.Name) + "\n")
		b.WriteString("      optional: true\n")
	}
	// The canonical form is the one the control plane produces when it
	// patches the image: a recompilation must not create a diff.
	return canonicalValues(b.String())
}

// AppRuntimeSecretName is the Kubernetes Secret the production pod mounts via envFrom.
// Kuberpack never creates or fills it; Infisical or kubectl does.
func AppRuntimeSecretName(app string) string {
	return "app-" + sanitizeName(app)
}

// DevSecretName is the shared, non-production Secret a preview mounts by
// default (inherit_secret_from: dev). An administrator syncs it from the
// Infisical `dev` environment.
const DevSecretName = "dev"

func internetEnabled(spec AppSpec) bool {
	if spec.Internet == nil {
		return true
	}
	return *spec.Internet
}

// networkPolicyEnabled reports whether the service is isolated. The platform
// decides by default; a manifest can opt a service out with defaultDeny:
// false.
func networkPolicyEnabled(spec AppSpec, p Platform) bool {
	if spec.NetworkPolicy != nil {
		return *spec.NetworkPolicy
	}
	return p.NetworkPolicy
}

// envYAML renders the `env` list of the values, without the indentation of
// the block itself.
func envYAML(spec AppSpec) string {
	var b strings.Builder
	// The preview's own database URL comes first: it is the value the
	// developer expects to win over an inherited one.
	if spec.Preview && spec.PreviewDatabase {
		b.WriteString(previewDatabaseEnvYAML(spec))
	}
	for _, e := range spec.Env {
		if strings.TrimSpace(e.Name) == "" {
			continue
		}
		b.WriteString("- name: " + e.Name + "\n")
		if e.SecretName != "" {
			b.WriteString("  valueFrom:\n")
			b.WriteString("    secretKeyRef:\n")
			b.WriteString("      name: " + e.SecretName + "\n")
			b.WriteString("      key: " + e.SecretKey + "\n")
			continue
		}
		b.WriteString("  value: " + strconv.Quote(e.Value) + "\n")
	}
	return b.String()
}

// releaseNamespace is the namespace the resources of a service land in.
func releaseNamespace(spec AppSpec, p Platform) string {
	if ns := strings.TrimSpace(spec.Namespace); ns != "" {
		return ns
	}
	return p.ReleaseNamespace
}

// replicaCount is the number of instances of a service. A service without a
// declared count runs one instance, never zero.
func replicaCount(spec AppSpec) int {
	// A service that declares zero replicas is declared and not run. A
	// service that declares nothing gets one.
	if spec.Replicas != nil && *spec.Replicas >= 0 {
		return *spec.Replicas
	}
	return 1
}

func helmReleaseYAML(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	return fmt.Sprintf(`apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %s
  namespace: %s
spec:
  interval: 5m
  # The release does not wait for the pods: the timeout bounds the test
  # hook, not the rollout. A broken application must not hold the pipeline
  # for long.
  timeout: 2m
  chart:
    spec:
      chart: %s
      reconcileStrategy: Revision
      sourceRef:
        kind: %s
        name: %s
        namespace: %s
      interval: 5m
  valuesFrom:
    - kind: ConfigMap
      name: %s-values
      valuesKey: values.yaml
  install:
    # The release does not wait for the pods. A broken image must not hold
    # the pipeline: the next push applies its fix at once. The health of the
    # application is the business of the test hook below, which reports it
    # without blocking the next deployment.
    disableWait: true
    remediation:
      retries: 2
  upgrade:
    disableWait: true
    cleanupOnFail: true
    remediation:
      retries: 1
      strategy: rollback
      remediateLastFailure: true
  test:
    enable: true
`, spec.Name, releaseNamespace(spec, p), p.ChartRef, p.ChartSourceKind, p.SourceName, p.SourceNamespace, spec.Name)
}

func appKustomizationYAML(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	resources := "  - helmrelease.yaml"
	if spec.TLSSecretName != "" {
		resources += "\n  - certificate.yaml"
	}
	if p.ImagePullSecret != "" {
		resources += "\n  - rbac.yaml"
	}
	for _, volume := range spec.Volumes {
		// A volume that comes from an addon has its claim already: the
		// addon wrote it, in its own directory. Listing it here would
		// point at a file that does not exist.
		if volume.Size != "" && volume.Claim == "" {
			resources += "\n  - " + volume.Name + ".pvc.yaml"
		}
	}
	return fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: %s

resources:
%s

configMapGenerator:
  - name: %s-values
    files:
      - values.yaml

generatorOptions:
  disableNameSuffixHash: true
  labels:
    reconcile.fluxcd.io/watch: Enabled
`, releaseNamespace(spec, p), resources, spec.Name)
}

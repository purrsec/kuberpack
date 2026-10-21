package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

// EnvVar is a literal environment variable of the application.
type EnvVar struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

// AppSpec is what you describe. Kuberpack expands it to Helm + Kustomize.
type AppSpec struct {
	Name        string
	Hostname    string
	Port        int
	Healthcheck string
	Replicas    int
	Preview     bool
	Internet    *bool
	Peers       []string
	// Env are the literal environment variables of the application.
	Env []EnvVar
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
	MaxAttempts  int
	App          AppSpec
	Platform     Platform
}

func (req EnsureRequest) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(fetch.TokenHeaderArgs(req.HTTPToken), args...)...)
}

// EnsureApp writes values, HelmRelease and app kustomization if they are missing.
func EnsureApp(ctx context.Context, req EnsureRequest) (Result, error) {
	spec, err := normalizeSpec(req.App)
	if err != nil {
		return Result{}, err
	}
	if req.GitOpsURL == "" {
		return Result{}, fmt.Errorf("gitops URL is empty")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	platform := req.Platform.withDefaults()

	parent, err := os.MkdirTemp("", "kuberpack-register-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	result := Result{App: spec.Name}

	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
			return Result{}, err
		}

		changed, err := writeMissingAppFiles(work, spec, platform)
		if err != nil {
			return Result{}, err
		}
		secretPaths, secretChanged, err := ensureSecretCR(work, spec, platform)
		if err != nil {
			return Result{}, err
		}
		dbChanged, err := ensurePreviewDatabase(work, spec, platform)
		if err != nil {
			return Result{}, err
		}
		changed = changed || secretChanged || dbChanged
		if !changed {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return Result{}, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}

		add := append([]string{platform.AppDir(spec.Name)}, secretPaths...)
		if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "--"}, add...)...)...); err != nil {
			return Result{}, err
		}
		msg := fmt.Sprintf("register %s: %s", spec.Name, spec.Hostname)
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", msg)...); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
			lastErr = err
			if _, fetchErr := req.remoteGit(ctx, work, "fetch", "origin", req.GitOpsBranch); fetchErr != nil {
				return Result{}, err
			}
			local, _ := runGit(ctx, work, "rev-parse", "HEAD")
			remote, _ := runGit(ctx, work, "rev-parse", "origin/"+req.GitOpsBranch)
			if local != "" && local == remote {
				return Result{}, err
			}
			continue
		}
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		result.InfraCommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops push rejected after %d attempts", req.MaxAttempts)
	}
	return Result{}, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

// SyncRuntime rewrites the runtime contract (hostname, port, healthcheck,
// networkPolicy) in an existing values.yaml. It never changes image or track.
func SyncRuntime(ctx context.Context, req EnsureRequest) (Result, error) {
	spec, err := normalizeSpec(req.App)
	if err != nil {
		return Result{}, err
	}
	if req.GitOpsURL == "" {
		return Result{}, fmt.Errorf("gitops URL is empty")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	platform := req.Platform.withDefaults()
	parent, err := os.MkdirTemp("", "kuberpack-runtime-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	result := Result{App: spec.Name}
	valuesRel := platform.ValuesPath(spec.Name)
	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
			return Result{}, err
		}
		path := filepath.Join(work, filepath.FromSlash(valuesRel))
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			if _, err := writeMissingAppFiles(work, spec, platform); err != nil {
				return Result{}, err
			}
		} else if err != nil {
			return Result{}, err
		} else {
			patched, err := PatchRuntime(raw, RuntimePatch{
				Hostname:      spec.Hostname,
				Port:          spec.Port,
				Healthcheck:   spec.Healthcheck,
				Internet:      spec.Internet,
				Peers:         spec.Peers,
				NetworkPolicy: &platform.NetworkPolicy,
			})
			if err != nil {
				return Result{}, err
			}
			if err := os.WriteFile(path, patched, 0o644); err != nil {
				return Result{}, err
			}
		}
		secretPaths, _, err := ensureSecretCR(work, spec, platform)
		if err != nil {
			return Result{}, err
		}
		if _, err := ensurePreviewDatabase(work, spec, platform); err != nil {
			return Result{}, err
		}

		add := append([]string{valuesRel}, secretPaths...)
		status, err := runGit(ctx, work, append([]string{"status", "--porcelain", "--"}, add...)...)
		if err != nil {
			return Result{}, err
		}
		if status == "" {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return Result{}, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}
		if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "--"}, add...)...)...); err != nil {
			return Result{}, err
		}
		msg := fmt.Sprintf("runtime %s", spec.Name)
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", msg)...); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
			lastErr = err
			continue
		}
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		result.InfraCommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops push rejected after %d attempts", req.MaxAttempts)
	}
	return Result{}, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

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
	if spec.Replicas <= 0 {
		spec.Replicas = 1
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
	dir := filepath.Join(work, filepath.FromSlash(p.AppDir(spec.Name)))
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

func enableInParent(work string, name string, p Platform) (bool, error) {
	p = p.withDefaults()
	path := filepath.Join(work, filepath.FromSlash(p.ParentPath()))
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
		return false, fmt.Errorf("read %s: %w", p.ParentPath(), err)
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
	b.WriteString("image: \"\"\n")
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
	if p.IngressTLS && p.TraefikCertResolver != "" {
		b.WriteString("    traefik.ingress.kubernetes.io/router.tls.certresolver: " + p.TraefikCertResolver + "\n")
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
	if spec.Preview {
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
	return b.String()
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
	if spec.Replicas > 0 {
		return spec.Replicas
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
  timeout: 5m
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
    remediation:
      retries: 6
  upgrade:
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
	return fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: %s

resources:
  - helmrelease.yaml

configMapGenerator:
  - name: %s-values
    files:
      - values.yaml

generatorOptions:
  disableNameSuffixHash: true
  labels:
    reconcile.fluxcd.io/watch: Enabled
`, releaseNamespace(spec, p), spec.Name)
}

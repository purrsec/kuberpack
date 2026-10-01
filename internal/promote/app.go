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

// AppSpec is what you describe. Kuberpack expands it to Helm + Kustomize.
type AppSpec struct {
	Name        string
	Hostname    string
	Port        int
	Healthcheck string
	Replicas    int
	Preview     bool
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
		if !changed {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return Result{}, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}

		rel := platform.AppDir(spec.Name)
		if _, err := runGit(ctx, work, gitConfig("add", "--", rel)...); err != nil {
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
		if spec.Preview {
			spec.Replicas = 1
		} else {
			spec.Replicas = 1
		}
	}
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
	b.WriteString("# Desired runtime. Kuberpack only rewrites `image`.\n")
	b.WriteString("track: main\n")
	b.WriteString("image: \"\"\n")
	b.WriteString("port: " + strconv.Itoa(spec.Port) + "\n")
	b.WriteString("healthcheck: " + spec.Healthcheck + "\n")
	b.WriteString("hostname: " + spec.Hostname + "\n")
	b.WriteString("replicas: " + strconv.Itoa(spec.Replicas) + "\n")
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
	if p.TraefikCertResolver != "" {
		b.WriteString("    traefik.ingress.kubernetes.io/router.tls.certresolver: " + p.TraefikCertResolver + "\n")
	}
	b.WriteString("networkPolicy:\n")
	if p.NetworkPolicy {
		b.WriteString("  enabled: true\n")
	} else {
		b.WriteString("  enabled: false\n")
	}
	if spec.Preview {
		b.WriteString("resources:\n")
		b.WriteString("  requests:\n")
		b.WriteString("    cpu: 25m\n")
		b.WriteString("    memory: 32Mi\n")
		b.WriteString("  limits:\n")
		b.WriteString("    cpu: 200m\n")
		b.WriteString("    memory: 128Mi\n")
	} else {
		b.WriteString("envFrom:\n")
		b.WriteString("  - secretRef:\n")
		b.WriteString("      name: " + AppRuntimeSecretName(spec.Name) + "\n")
	}
	return b.String()
}

// AppRuntimeSecretName is the Kubernetes Secret the production pod mounts via envFrom.
// Kuberpack never creates or fills it; Infisical or kubectl does.
func AppRuntimeSecretName(app string) string {
	return "app-" + sanitizeName(app)
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
        kind: GitRepository
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
`, spec.Name, p.ReleaseNamespace, p.ChartRef, p.SourceName, p.SourceNamespace, spec.Name)
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
`, p.ReleaseNamespace, spec.Name)
}

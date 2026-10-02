package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"gopkg.in/yaml.v3"
)

func fetchTokenArgs(token string) []string { return fetch.TokenHeaderArgs(token) }

// ProjectManifest is the human-authored source compiled into GitOps primitives.
type ProjectManifest struct {
	Project   string             `yaml:"project" json:"project"`
	Namespace string             `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Services  map[string]Service `yaml:"services,omitempty" json:"services,omitempty"`
	Addons    map[string]Addon   `yaml:"addons,omitempty" json:"addons,omitempty"`
	Policies  Policy             `yaml:"policies,omitempty" json:"policies,omitempty"`
}

type Service struct {
	Type        string   `yaml:"type,omitempty" json:"type,omitempty"` // stateless
	Repository  string   `yaml:"repository,omitempty" json:"repository,omitempty"`
	Port        int      `yaml:"port,omitempty" json:"port,omitempty"`
	Hostname    string   `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Healthcheck string   `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Secrets     []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	SecretEnv   string   `yaml:"secret_env,omitempty" json:"secret_env,omitempty"`
}

type Addon struct {
	Engine   string `yaml:"engine,omitempty" json:"engine,omitempty"` // cnpg
	Database string `yaml:"database,omitempty" json:"database,omitempty"`
	Owner    string `yaml:"owner,omitempty" json:"owner,omitempty"`
}

type Policy struct {
	DefaultDeny bool     `yaml:"defaultDeny,omitempty" json:"defaultDeny,omitempty"`
	Internet    *bool    `yaml:"internet,omitempty" json:"internet,omitempty"`
	Peers       []string `yaml:"peers,omitempty" json:"peers,omitempty"`
}

// ProjectRequest compiles a manifest into the GitOps repository.
type ProjectRequest struct {
	GitOpsURL    string
	GitOpsBranch string
	HTTPToken    string
	MaxAttempts  int
	Manifest     ProjectManifest
	Platform     Platform
	// Raw is the caller's exact manifest text; written verbatim so comments
	// and ordering survive. Falls back to a re-marshal when empty.
	Raw string
}

type ProjectResult struct {
	Project        string
	Services       []string
	Addons         []string
	InfraCommitSHA string
	Changed        bool
}

func (req ProjectRequest) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(fetchTokenArgs(req.HTTPToken), args...)...)
}

// --- platform paths -------------------------------------------------------

func (p Platform) ProjectsDirPath() string {
	d := strings.TrimSpace(p.ProjectsDir)
	if d == "" {
		d = "kubernetes/vps/projects"
	}
	return d
}

func (p Platform) ProjectDir(name string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectsDirPath(), sanitizeName(name)))
}

func (p Platform) ProjectManifestPath(name string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectDir(name), "kuberpack.yaml"))
}

func (p Platform) ProjectParentPath(name string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectDir(name), "kustomization.yaml"))
}

// serviceDir returns the directory of a service inside the project.
func (p Platform) projectServiceDir(project, service string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectDir(project), sanitizeName(service)))
}

// --- compile --------------------------------------------------------------

// EnsureProject compiles a manifest and commits the result. It never rewrites
// an existing values.yaml, so a digest already pinned by promote is preserved.
func EnsureProject(ctx context.Context, req ProjectRequest) (ProjectResult, error) {
	if req.GitOpsURL == "" {
		return ProjectResult{}, fmt.Errorf("gitops URL is empty")
	}
	if strings.TrimSpace(req.Manifest.Project) == "" {
		return ProjectResult{}, fmt.Errorf("project is required")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	project := sanitizeName(req.Manifest.Project)
	result := ProjectResult{Project: project}

	parent, err := os.MkdirTemp("", "kuberpack-project-*")
	if err != nil {
		return ProjectResult{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")

	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return ProjectResult{}, err
		}
		if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
			return ProjectResult{}, err
		}
		services, adds, err := compileProjectFiles(work, req.Manifest, req.Platform, req.Raw)
		if err != nil {
			return ProjectResult{}, err
		}
		result.Services = services
		result.Addons = adds

		rel := req.Platform.ProjectDir(project)
		status, err := runGit(ctx, work, "status", "--porcelain", "--", rel)
		if err != nil {
			return ProjectResult{}, err
		}
		if status == "" {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return ProjectResult{}, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}
		if _, err := runGit(ctx, work, gitConfig("add", "--", rel)...); err != nil {
			return ProjectResult{}, err
		}
		msg := fmt.Sprintf("project %s: %d services, %d addons", project, len(services), len(adds))
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", msg)...); err != nil {
			return ProjectResult{}, err
		}
		if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
			lastErr = err
			if _, fetchErr := req.remoteGit(ctx, work, "fetch", "origin", req.GitOpsBranch); fetchErr != nil {
				return ProjectResult{}, err
			}
			local, _ := runGit(ctx, work, "rev-parse", "HEAD")
			remote, _ := runGit(ctx, work, "rev-parse", "origin/"+req.GitOpsBranch)
			if local != "" && local == remote {
				return ProjectResult{}, err
			}
			continue
		}
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return ProjectResult{}, err
		}
		result.InfraCommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops push rejected after %d attempts", req.MaxAttempts)
	}
	return ProjectResult{}, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

// compileProjectFiles writes the project source + generated artifacts.
func compileProjectFiles(work string, m ProjectManifest, p Platform, raw string) ([]string, []string, error) {
	p = p.withDefaults()
	project := sanitizeName(m.Project)
	projectDir := filepath.Join(work, filepath.FromSlash(p.ProjectDir(project)))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, nil, err
	}

	// Persist the human source verbatim (so comments survive).
	manifestText := strings.TrimSpace(raw)
	if manifestText == "" {
		b, err := yaml.Marshal(m)
		if err != nil {
			return nil, nil, err
		}
		manifestText = string(b)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "kuberpack.yaml"), []byte(manifestText+"\n"), 0o644); err != nil {
		return nil, nil, err
	}

	net := m.Policies.Internet
	servicePeers := projectPeers(m)

	services := make([]string, 0, len(m.Services))
	for name, svc := range m.Services {
		sname := sanitizeName(name)
		spec := AppSpec{
			Name:        sname,
			Hostname:    svc.Hostname,
			Port:        svc.Port,
			Healthcheck: svc.Healthcheck,
			Internet:    net,
			Peers:       servicePeers,
			Secrets:     svc.Secrets,
			SecretEnv:   svc.SecretEnv,
		}
		dir := filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, sname)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		// Only write missing files: an existing values.yaml keeps its pinned
		// image (promote owns image/track).
		if err := writeIfMissing(filepath.Join(dir, "values.yaml"), RenderValues(spec, p)); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "helmrelease.yaml"), RenderHelmRelease(spec, p)); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "kustomization.yaml"), RenderAppKustomization(spec, p)); err != nil {
			return nil, nil, err
		}
		if len(svc.Secrets) > 0 && p.InfisicalEnabled() {
			if err := writeIfMissing(filepath.Join(dir, "secret.yaml"), RenderSecretCR(spec, p)); err != nil {
				return nil, nil, err
			}
		}
		services = append(services, sname)
	}
	sort.Strings(services)

	addons := make([]string, 0, len(m.Addons))
	for name, addon := range m.Addons {
		if strings.ToLower(strings.TrimSpace(addon.Engine)) != "cnpg" {
			continue
		}
		aname := sanitizeName(name)
		dir := filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, aname)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "database.yaml"), RenderProjectPostgres(project, aname, addon, p)); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n\nresources:\n  - database.yaml\n"); err != nil {
			return nil, nil, err
		}
		addons = append(addons, aname)
	}
	sort.Strings(addons)

	resources := append(append([]string{}, services...), addons...)
	parentYAML := RenderParentKustomization(m.Namespace, resources)
	if err := os.WriteFile(filepath.Join(projectDir, "kustomization.yaml"), []byte(parentYAML), 0o644); err != nil {
		return nil, nil, err
	}
	return services, addons, nil
}

// projectPeers turns manifest policy peers into chart peer strings. A bare
// "postgres"/"valkey" name targets that addon in the same project.
func projectPeers(m ProjectManifest) []string {
	project := sanitizeName(m.Project)
	var out []string
	for _, peer := range m.Policies.Peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		if _, isAddon := m.Addons[peer]; isAddon {
			out = append(out, "postgres:"+project)
			continue
		}
		out = append(out, peer)
	}
	return out
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// RenderProjectPostgres renders a CNPG Cluster for a project addon, with the
// labels the stateless chart's `postgres:<project>` peer expects.
func RenderProjectPostgres(project, addon string, a Addon, p Platform) string {
	p = p.withDefaults()
	db := strings.TrimSpace(a.Database)
	if db == "" {
		db = "app"
	}
	owner := strings.TrimSpace(a.Owner)
	if owner == "" {
		owner = "app"
	}
	name := sanitizeName(project) + "-" + sanitizeName(addon)
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("apiVersion: postgresql.cnpg.io/v1\n")
	b.WriteString("kind: Cluster\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + p.ReleaseNamespace + "\n")
	b.WriteString("  labels:\n")
	b.WriteString("    kuberpack.io/addon: postgres\n")
	b.WriteString("    kuberpack.io/for: " + sanitizeName(project) + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  instances: 1\n")
	b.WriteString("  imageName: " + p.PreviewDatabaseImage + "\n")
	b.WriteString("  imagePullPolicy: IfNotPresent\n")
	b.WriteString("  inheritedMetadata:\n")
	b.WriteString("    labels:\n")
	b.WriteString("      kuberpack.io/addon: postgres\n")
	b.WriteString("      kuberpack.io/for: " + sanitizeName(project) + "\n")
	b.WriteString("  primaryUpdateStrategy: unsupervised\n")
	b.WriteString("  enableSuperuserAccess: false\n")
	b.WriteString("  bootstrap:\n")
	b.WriteString("    initdb:\n")
	b.WriteString("      database: " + db + "\n")
	b.WriteString("      owner: " + owner + "\n")
	b.WriteString("      encoding: UTF8\n")
	b.WriteString("      dataChecksums: true\n")
	b.WriteString("  storage:\n")
	b.WriteString("    storageClass: " + p.PreviewDatabaseClass + "\n")
	b.WriteString("    size: " + p.PreviewDatabaseStorage + "\n")
	b.WriteString("  resources:\n")
	b.WriteString("    requests: { cpu: 100m, memory: 256Mi }\n")
	b.WriteString("    limits: { cpu: \"1\", memory: 1Gi }\n")
	b.WriteString("  monitoring:\n")
	b.WriteString("    enablePodMonitor: false\n")
	return b.String()
}

// ErrProjectNotFound is returned when a project manifest is absent.
var ErrProjectNotFound = fmt.Errorf("project not found")

// ReadProjectManifest fetches the raw kuberpack.yaml of a project.
func ReadProjectManifest(ctx context.Context, req ProjectRequest) ([]byte, error) {
	project := sanitizeName(req.Manifest.Project)
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if req.GitOpsURL == "" {
		return nil, fmt.Errorf("gitops URL is empty")
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	parent, err := os.MkdirTemp("", "kuberpack-project-read-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(req.Platform.ProjectManifestPath(project))))
	if os.IsNotExist(err) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// DeleteProject removes the project from the GitOps repository.
func DeleteProject(ctx context.Context, req ProjectRequest) (ProjectResult, error) {
	project := sanitizeName(req.Manifest.Project)
	if project == "" {
		return ProjectResult{}, fmt.Errorf("project is required")
	}
	if req.GitOpsURL == "" {
		return ProjectResult{}, fmt.Errorf("gitops URL is empty")
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	parent, err := os.MkdirTemp("", "kuberpack-project-rm-*")
	if err != nil {
		return ProjectResult{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
		return ProjectResult{}, err
	}
	rel := req.Platform.ProjectDir(project)
	if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(rel))); os.IsNotExist(err) {
		return ProjectResult{Project: project}, nil
	}
	if err := os.RemoveAll(filepath.Join(work, filepath.FromSlash(rel))); err != nil {
		return ProjectResult{}, err
	}
	if _, err := runGit(ctx, work, gitConfig("add", "-A", "--", rel)...); err != nil {
		return ProjectResult{}, err
	}
	if _, err := runGit(ctx, work, gitConfig("commit", "-m", "unregister project "+project)...); err != nil {
		return ProjectResult{}, err
	}
	if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
		return ProjectResult{}, err
	}
	sha, err := runGit(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return ProjectResult{}, err
	}
	return ProjectResult{Project: project, InfraCommitSHA: sha, Changed: true}, nil
}

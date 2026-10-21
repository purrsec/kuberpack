package promote

import (
	"bytes"
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

// Service is one application of a project.
type Service struct {
	Type string `yaml:"type,omitempty" json:"type,omitempty"` // stateless
	// Repository is the Forgejo repository the image is built from. It is
	// the entry point of the build pipeline: a service without one is never
	// built.
	Repository string `yaml:"repository,omitempty" json:"repository,omitempty"`
	// Branch is the branch whose pushes become the production. Defaults to
	// the default branch of the repository.
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// Strategy is auto, uv or railpack.
	Strategy    string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Port        int    `yaml:"port,omitempty" json:"port,omitempty"`
	Hostname    string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Healthcheck string `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Replicas    int    `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Secrets     []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	SecretEnv   string   `yaml:"secret_env,omitempty" json:"secret_env,omitempty"`
	// Env are literal environment variables of the application.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Build holds the build-time overrides of the service.
	Build *BuildOptions `yaml:"build,omitempty" json:"build,omitempty"`
}

// BuildOptions are the build-time overrides of a service.
type BuildOptions struct {
	RootDirectory string `yaml:"root_directory,omitempty" json:"root_directory,omitempty"`
	BuildCommand  string `yaml:"build_command,omitempty" json:"build_command,omitempty"`
	StartCommand  string `yaml:"start_command,omitempty" json:"start_command,omitempty"`
}

// Addon is an infrastructure dependency of a project.
type Addon struct {
	Engine   string  `yaml:"engine,omitempty" json:"engine,omitempty"` // cnpg
	Database string  `yaml:"database,omitempty" json:"database,omitempty"`
	Owner    string  `yaml:"owner,omitempty" json:"owner,omitempty"`
	Backup   *Backup `yaml:"backup,omitempty" json:"backup,omitempty"`
}

// Backup describes how an addon is backed up.
type Backup struct {
	// S3 is the name of the storage profile of the platform. It is not
	// implemented yet: the compiler refuses it.
	S3 string `yaml:"s3,omitempty" json:"s3,omitempty"`
	// Retention is how long the backups are kept (30d, 7d...).
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// Policy is the network policy of the project.
type Policy struct {
	// DefaultDeny isolates the services of the project. It defaults to true:
	// a service only reaches what the manifest allows.
	DefaultDeny *bool   `yaml:"defaultDeny,omitempty" json:"defaultDeny,omitempty"`
	Internet    *bool   `yaml:"internet,omitempty" json:"internet,omitempty"`
	Peers       []string `yaml:"peers,omitempty" json:"peers,omitempty"`
}

// defaultDenyEnabled reports whether the project isolates its services.
func (p Policy) defaultDenyEnabled() bool {
	if p.DefaultDeny == nil {
		return true
	}
	return *p.DefaultDeny
}

// BoolPtr returns a pointer to a bool, for the optional fields of a manifest.
func BoolPtr(v bool) *bool { return &v }

// ParseProjectManifest decodes a manifest strictly: an unknown field is an
// error. A manifest and the compiler must never drift apart silently.
func ParseProjectManifest(raw []byte) (ProjectManifest, error) {
	var m ProjectManifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := validateManifest(m); err != nil {
		return m, err
	}
	return m, nil
}

// validateManifest refuses what the compiler does not implement. A manifest
// that declares something unsupported fails loudly instead of compiling into
// a silent no-op.
func validateManifest(m ProjectManifest) error {
	if strings.TrimSpace(m.Project) == "" {
		return fmt.Errorf("project is required")
	}
	for name, svc := range m.Services {
		switch strings.ToLower(strings.TrimSpace(svc.Type)) {
		case "", "stateless":
		default:
			return fmt.Errorf("service %q: type %q is not supported (stateless)", name, svc.Type)
		}
		if repo := strings.TrimSpace(svc.Repository); repo != "" && !strings.Contains(repo, "/") {
			return fmt.Errorf("service %q: repository %q must be owner/name", name, repo)
		}
		if s := strings.TrimSpace(svc.Strategy); s != "" {
			switch s {
			case "auto", "uv", "railpack":
			default:
				return fmt.Errorf("service %q: strategy %q is not supported (auto, uv, railpack)", name, s)
			}
		}
	}
	for name, addon := range m.Addons {
		switch strings.ToLower(strings.TrimSpace(addon.Engine)) {
		case "cnpg":
		default:
			return fmt.Errorf("addon %q: engine %q is not supported (cnpg)", name, addon.Engine)
		}
		if addon.Backup != nil && strings.TrimSpace(addon.Backup.S3) != "" {
			return fmt.Errorf("addon %q: backup.s3 is not supported yet", name)
		}
	}
	for _, peer := range m.Policies.Peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		if _, ok := m.Addons[peer]; ok {
			continue
		}
		if _, ok := m.Services[peer]; ok {
			continue
		}
		return fmt.Errorf("policies.peers: %q is neither a service nor an addon of the project", peer)
	}
	return nil
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
	Namespace      string
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

// ProjectsParentPath is the kustomization listing the projects. Flux only
// reconciles a project once it is listed there.
func (p Platform) ProjectsParentPath() string {
	return filepath.ToSlash(filepath.Join(p.ProjectsDirPath(), "kustomization.yaml"))
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
		parentRel := req.Platform.ProjectsParentPath()
		status, err := runGit(ctx, work, "status", "--porcelain", "--", rel, parentRel)
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
		if _, err := runGit(ctx, work, gitConfig("add", "--", rel, parentRel)...); err != nil {
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
	namespace := projectNamespace(m, project)
	// The NetworkPolicy of the services follows the policy of the project.
	isolated := m.Policies.defaultDenyEnabled()

	services := make([]string, 0, len(m.Services))
	for name, svc := range m.Services {
		sname := sanitizeName(name)
		spec := AppSpec{
			Name:          sname,
			Hostname:      svc.Hostname,
			Port:          svc.Port,
			Healthcheck:   svc.Healthcheck,
			Replicas:      svc.Replicas,
			Internet:      net,
			Peers:         servicePeers,
			Env:           serviceEnv(svc),
			Secrets:       svc.Secrets,
			SecretEnv:     svc.SecretEnv,
			Project:       project,
			Namespace:     namespace,
			NetworkPolicy: &isolated,
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
		aname := sanitizeName(name)
		dir := filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, aname)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "database.yaml"), RenderProjectPostgres(project, aname, addon, namespace, p)); err != nil {
			return nil, nil, err
		}
		if err := writeIfMissing(filepath.Join(dir, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n\nresources:\n  - database.yaml\n"); err != nil {
			return nil, nil, err
		}
		addons = append(addons, aname)
	}
	sort.Strings(addons)

	// A service whose image is not pinned yet is not activated: Flux would
	// otherwise install a HelmRelease whose chart requires a digest.
	activated := make([]string, 0, len(services))
	for _, sname := range services {
		if serviceHasPinnedImage(filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, sname)))) {
			activated = append(activated, sname)
		}
	}
	resources := append(append([]string{}, activated...), addons...)
	// The namespace of the project is created by the project itself: Flux
	// applies it with the rest.
	if err := writeIfMissing(filepath.Join(projectDir, "namespace.yaml"), RenderProjectNamespace(namespace)); err != nil {
		return nil, nil, err
	}
	parentYAML := RenderParentKustomization(namespace, append([]string{"namespace.yaml"}, resources...))
	if err := os.WriteFile(filepath.Join(projectDir, "kustomization.yaml"), []byte(parentYAML), 0o644); err != nil {
		return nil, nil, err
	}

	// The projects parent lists the project: Flux only reconciles a project
	// once it is referenced there.
	parentPath := filepath.Join(work, filepath.FromSlash(p.ProjectsParentPath()))
	parentRaw, err := os.ReadFile(parentPath)
	if os.IsNotExist(err) {
		parentRaw = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	} else if err != nil {
		return nil, nil, err
	}
	if !listedInKustomization(parentRaw, project) {
		parentRaw = appendKustomizationResource(parentRaw, project)
	}
	if err := os.WriteFile(parentPath, parentRaw, 0o644); err != nil {
		return nil, nil, err
	}
	return services, addons, nil
}

// projectNamespace is the namespace the resources of a project land in. It
// defaults to the name of the project.
func projectNamespace(m ProjectManifest, project string) string {
	if ns := strings.TrimSpace(m.Namespace); ns != "" {
		return sanitizeName(ns)
	}
	return project
}

// serviceEnv turns the literal variables of a service into a sorted list:
// the compiler must be deterministic, a map is not.
func serviceEnv(svc Service) []EnvVar {
	if len(svc.Env) == 0 {
		return nil
	}
	names := make([]string, 0, len(svc.Env))
	for name := range svc.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]EnvVar, 0, len(names))
	for _, name := range names {
		out = append(out, EnvVar{Name: name, Value: svc.Env[name]})
	}
	return out
}

// RenderProjectNamespace renders the Namespace object of a project.
func RenderProjectNamespace(namespace string) string {
	return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + namespace +
		"\n  labels:\n    app.kubernetes.io/part-of: kuberpack\n"
}

// serviceHasPinnedImage reports whether the service already carries a
// digest-pinned image. Only the control plane may write it.
func serviceHasPinnedImage(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		return false
	}
	meta, err := ParseValuesMeta(raw)
	if err != nil {
		return false
	}
	return strings.TrimSpace(meta.Image) != ""
}

// projectPeers turns manifest policy peers into chart peer strings. A bare
// addon name targets that addon of the project ("postgres:<project>"), a
// service name targets the service itself.
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
		out = append(out, sanitizeName(peer))
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
func RenderProjectPostgres(project, addon string, a Addon, namespace string, p Platform) string {
	p = p.withDefaults()
	db := strings.TrimSpace(a.Database)
	if db == "" {
		db = "app"
	}
	owner := strings.TrimSpace(a.Owner)
	if owner == "" {
		owner = "app"
	}
	if strings.TrimSpace(namespace) == "" {
		namespace = p.ReleaseNamespace
	}
	name := sanitizeName(project) + "-" + sanitizeName(addon)
	var b strings.Builder
	b.WriteString("# Generated by Kuberpack from the project manifest.\n")
	b.WriteString("apiVersion: postgresql.cnpg.io/v1\n")
	b.WriteString("kind: Cluster\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
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
	// The retention of the backups is the only part of the backup contract
	// the compiler renders: the S3 profile is a platform decision.
	if a.Backup != nil {
		if retention := strings.TrimSpace(a.Backup.Retention); retention != "" {
			b.WriteString("  backup:\n")
			b.WriteString("    retentionPolicy: " + retention + "\n")
		}
	}
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

// ListProjects returns the projects present in the GitOps repository, with
// their manifest when it can be read.
func ListProjects(ctx context.Context, req ProjectRequest) ([]ProjectResult, error) {
	if req.GitOpsURL == "" {
		return nil, fmt.Errorf("gitops URL is empty")
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	parent, err := os.MkdirTemp("", "kuberpack-project-list-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
		return nil, err
	}
	root := filepath.Join(work, filepath.FromSlash(req.Platform.ProjectsDirPath()))
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []ProjectResult{}, nil
		}
		return nil, err
	}
	out := make([]ProjectResult, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := sanitizeName(entry.Name())
		if name == "" {
			continue
		}
		item := ProjectResult{Project: name}
		raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(req.Platform.ProjectManifestPath(name))))
		if err == nil {
			var m ProjectManifest
			if yaml.Unmarshal(raw, &m) == nil {
				if ns := strings.TrimSpace(m.Namespace); ns != "" {
					item.Namespace = ns
				}
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
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
	parentRel := req.Platform.ProjectsParentPath()
	parentPath := filepath.Join(work, filepath.FromSlash(parentRel))
	add := []string{}
	changed := false
	if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(rel))); err == nil {
		if err := os.RemoveAll(filepath.Join(work, filepath.FromSlash(rel))); err != nil {
			return ProjectResult{}, err
		}
		add = append(add, rel)
		changed = true
	} else if !os.IsNotExist(err) {
		return ProjectResult{}, err
	}
	// Drop the project from the projects parent so Flux prunes it.
	if raw, err := os.ReadFile(parentPath); err == nil {
		if listedInKustomization(raw, project) {
			if err := os.WriteFile(parentPath, removeKustomizationResource(raw, project), 0o644); err != nil {
				return ProjectResult{}, err
			}
			add = append(add, parentRel)
			changed = true
		}
	}
	if !changed {
		return ProjectResult{Project: project}, nil
	}
	if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "-A", "--"}, add...)...)...); err != nil {
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

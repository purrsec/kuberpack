package promote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"gopkg.in/yaml.v3"
)

type ProjectManifest = spec.ProjectManifest
type InfisicalScope = spec.InfisicalScope
type Service = spec.Service
type ServiceResources = spec.ServiceResources
type Volume = spec.Volume
type BuildOptions = spec.BuildOptions
type Addon = spec.Addon
type Backup = spec.Backup
type Policy = spec.Policy

func BoolPtr(v bool) *bool { return spec.BoolPtr(v) }

// ParseProjectManifest decodes a manifest strictly: an unknown field is an
// error. A manifest and the compiler must never drift apart silently.
func ParseProjectManifest(raw []byte) (ProjectManifest, error) {
	var m ProjectManifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("invalid manifest: expected a single YAML document")
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
	if err := validateManifestFields(m); err != nil {
		return err
	}
	if strings.TrimSpace(m.Project) == "" {
		return fmt.Errorf("project is required")
	}
	if !m.Policies.DefaultDenyEnabled() {
		return fmt.Errorf("policies.defaultDeny: false is not allowed for an isolated project")
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
		if strings.TrimSpace(svc.Repository) == "" && strings.TrimSpace(svc.Image) == "" {
			return fmt.Errorf("service %q: it must declare a repository to build, or an image to run", name)
		}
		if strings.TrimSpace(svc.Repository) != "" && strings.TrimSpace(svc.Image) != "" {
			return fmt.Errorf("service %q: choose either repository or image", name)
		}
		if svc.Image != "" && (svc.Build != nil || svc.Strategy != "" || svc.Branch != "") {
			return fmt.Errorf("service %q: build, strategy and branch require a repository", name)
		}
		if s := strings.TrimSpace(svc.Strategy); s != "" {
			switch s {
			case "auto", "uv", "railpack":
			default:
				return fmt.Errorf("service %q: strategy %q is not supported (auto, uv, railpack)", name, s)
			}
		}
		for _, addon := range svc.Addons {
			if _, ok := m.Addons[strings.TrimSpace(addon)]; !ok {
				return fmt.Errorf("service %q: addon %q is not declared in the project", name, addon)
			}
		}
		for _, peer := range servicePeerNames(m, svc) {
			if _, ok := m.Services[peer]; ok {
				continue
			}
			if _, ok := m.Addons[peer]; !ok {
				return fmt.Errorf("service %q: peer %q is not declared in this project", name, peer)
			}
		}
	}
	for name, addon := range m.Addons {
		if _, ok := AddonEngineFor(addon.Engine); !ok {
			return fmt.Errorf("addon %q: engine %q is not available (%s)",
				name, addon.Engine, strings.Join(AddonEngineNames(), ", "))
		}
		if addon.Backup != nil && strings.TrimSpace(addon.Backup.S3) != "" {
			// The backup goes to an object storage: the project must
			// declare one, and it must be of a kind that speaks S3.
			storage, ok := m.Addons[strings.TrimSpace(addon.Backup.S3)]
			if !ok {
				return fmt.Errorf("addon %q: the backup goes to %q, which the project does not declare",
					name, addon.Backup.S3)
			}
			entry, known := AddonCatalogEntryForEngine(storage.Engine)
			if !known || entry.Kind != "s3" {
				return fmt.Errorf("addon %q: the backup goes to %q, which does not speak S3",
					name, addon.Backup.S3)
			}
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
	// AuthArgs are the git arguments that authenticate the clone of the
	// GitOps repository. Every forge has its own way.
	AuthArgs []string
	// AuthScheme is how the forge of the GitOps repository authenticates a
	// clone: "token" for a Gitea, "basic" for GitHub and GitLab.
	AuthScheme           string
	MaxAttempts          int
	ExpectedManifestHash *string
	Manifest             ProjectManifest
	Platform             Platform
	// Force removes the project even when it still holds a database. The
	// data goes with it.
	Force bool
	// Raw is the caller's exact manifest text; written verbatim so comments
	// and ordering survive. Falls back to a re-marshal when empty.
	Raw string
	// NamespaceCheck is called before any write, with the project and the
	// namespace it wants. It lets the control plane refuse the implicit
	// adoption of a namespace that belongs to somebody else.
	NamespaceCheck func(project, namespace string) error
}

// authArgs is how the clone of the GitOps repository is authenticated.
func (req ProjectRequest) authArgs() []string {
	if req.AuthArgs != nil {
		return req.AuthArgs
	}
	return fetch.AuthArgs(req.AuthScheme, req.HTTPToken)
}

// ProjectApp is the operational description of a service of a project: what
// to build, and where the digest must land. The control plane reconciles it
// into either a repository build target or a published-image inventory entry.
// Only repository services receive an HMAC secret, webhook and build.
type ProjectApp struct {
	Image string
	// Name is the global identity of the app: <project>-<service>.
	Name string
	// Service is the name of the service inside the project.
	Service string
	// Repository is the Forgejo repository the image is built from.
	Repository string
	// Branch is the branch whose pushes become the production.
	Branch string
	// Strategy is auto, uv or railpack.
	Strategy      string
	Hostname      string
	Port          int
	Healthcheck   string
	StartCommand  string
	RootDirectory string
	BuildCommand  string
	Secrets       []string
	SecretEnv     string
	// Infisical overrides the platform-wide secret scope for this project.
	InfisicalProjectID string
	InfisicalAuthRef   string
	InfisicalPath      string
	Internet           *bool
	Peers              []string
	Namespace          string
	// ValuesPath is where the digest of the build must be written, relative
	// to the root of the GitOps repository.
	ValuesPath string
}

type ProjectResult struct {
	Project   string
	Namespace string
	Services  []string
	Addons    []string
	// Apps are the services that have a repository: the control plane
	// reconciles them into the build pipeline.
	Apps           []ProjectApp
	InfraCommitSHA string
	Changed        bool
}

func (req ProjectRequest) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(req.authArgs(), args...)...)
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

// projectServiceDir returns the directory of a service inside the project.
func (p Platform) projectServiceDir(project, service string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectDir(project), sanitizeName(service)))
}

// projectAddonDir returns the directory of an addon inside the project. The
// addons have their own directory: a service named like an addon would
// otherwise write over it.
func (p Platform) projectAddonDir(project, addon string) string {
	return filepath.ToSlash(filepath.Join(p.ProjectDir(project), "addons", sanitizeName(addon)))
}

// ProjectServiceValuesPath is where the digest of a service of a project is
// written, relative to the root of the GitOps repository.
func (p Platform) ProjectServiceValuesPath(project, service string) string {
	return filepath.ToSlash(filepath.Join(p.projectServiceDir(project, service), "values.yaml"))
}

// --- compile --------------------------------------------------------------

// EnsureProject regenerates runtime values from the manifest and commits them.
// A built image's pinned digest and track are preserved during recompilation.
func EnsureProject(ctx context.Context, req ProjectRequest) (ProjectResult, error) {
	if err := validateManifest(req.Manifest); err != nil {
		return ProjectResult{}, err
	}
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
	if req.NamespaceCheck != nil {
		if err := req.NamespaceCheck(project, ProjectNamespace(req.Manifest, project)); err != nil {
			return result, err
		}
	}

	// The project must fit in the node. A cluster that does not have
	// enough memory does not fail cleanly: the kernel kills the biggest
	// process, the DNS goes down, and everything stops reconciling.
	if err := checkCapacity(req.Manifest, req.Platform); err != nil {
		return result, err
	}

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
		if err := guardProjectTree(work, req.Platform); err != nil {
			return ProjectResult{}, err
		}
		if err := checkExpectedManifest(work, req); err != nil {
			return ProjectResult{}, err
		}
		if err := checkClusterAddition(work, req.Platform, req.Manifest); err != nil {
			return ProjectResult{}, err
		}
		services, adds, apps, err := compileProjectFiles(ctx, work, req.Manifest, req.Platform, req.Raw, true)
		if err != nil {
			return ProjectResult{}, err
		}
		result.Services = services
		result.Addons = adds
		result.Apps = apps
		if err := SyncInfisicalOperatorScope(work, req.Platform); err != nil {
			return ProjectResult{}, err
		}

		rel := req.Platform.ProjectDir(project)
		parentRel := req.Platform.ProjectsParentPath()
		// The Infisical objects live in their own directory, beside the
		// projects. They are part of the commit: a secret declared by the
		// manifest must reach the cluster, or the application starts
		// without its secret.
		watched := []string{rel, parentRel}
		platform := req.Platform.withDefaults()
		if platform.InfisicalOperatorScope {
			watched = append(watched, filepath.ToSlash(filepath.Join(platform.ProjectsDirPath(), InfisicalOperatorScopeFile)))
		}
		if platform.InfisicalEnabled() {
			if _, err := os.Stat(filepath.Join(work, platform.InfisicalDir)); err == nil {
				watched = append(watched, platform.InfisicalDir)
			}
			if _, err := os.Stat(filepath.Join(work, "kustomization.yaml")); err == nil {
				watched = append(watched, "kustomization.yaml")
			}
		}
		statusArgs := append([]string{"status", "--porcelain", "--"}, watched...)
		status, err := runGit(ctx, work, statusArgs...)
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
		addArgs := append(gitConfig("add", "--"), watched...)
		if _, err := runGit(ctx, work, addArgs...); err != nil {
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
func compileProjectFiles(ctx context.Context, work string, m ProjectManifest, p Platform, raw string, writeManifest bool) ([]string, []string, []ProjectApp, error) {
	p = p.withDefaults()
	if err := guardProjectTree(work, p); err != nil {
		return nil, nil, nil, err
	}
	if err := validateProjectNamespace(work, m, p); err != nil {
		return nil, nil, nil, err
	}
	for name, svc := range m.Services {
		if len(svc.Secrets) > 0 && !p.InfisicalEnabled() {
			return nil, nil, nil, fmt.Errorf("service %q: secrets are declared but Infisical is not configured on the platform", name)
		}
	}
	project := sanitizeName(m.Project)
	projectDir := filepath.Join(work, filepath.FromSlash(p.ProjectDir(project)))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, nil, nil, err
	}
	if old, err := os.ReadFile(filepath.Join(projectDir, "kuberpack.yaml")); err == nil {
		var previous ProjectManifest
		if err := yaml.Unmarshal(old, &previous); err != nil {
			return nil, nil, nil, err
		}
		for name := range previous.Services {
			if _, _, err := removeSecretCR(work, AppSpec{Name: name, Project: project}, p); err != nil {
				return nil, nil, nil, err
			}
		}
	}

	// Persist the human source verbatim (so comments survive).
	// The manifest is the source of truth. It is written only when it comes
	// from the API: when it is already in the repository, it is read from
	// there and left untouched.
	if writeManifest {
		manifestText := strings.TrimSpace(raw)
		if manifestText == "" {
			b, err := yaml.Marshal(m)
			if err != nil {
				return nil, nil, nil, err
			}
			manifestText = string(b)
		}
		if err := os.WriteFile(filepath.Join(projectDir, "kuberpack.yaml"), []byte(manifestText+"\n"), 0o644); err != nil {
			return nil, nil, nil, err
		}
	}

	net := m.Policies.Internet
	if net == nil {
		net = BoolPtr(false)
	}
	namespace := projectNamespace(m, project)
	if err := validateProjectSecretScope(m, p); err != nil {
		return nil, nil, nil, err
	}
	// The NetworkPolicy of the services follows the policy of the project.
	isolated := m.Policies.DefaultDenyEnabled()

	// The addons are rendered first: the services consume them.
	addons, addonResults, err := compileAddons(work, m, project, namespace, p)
	if err != nil {
		return nil, nil, nil, err
	}
	// The policy peers are either services of the project or addons of the
	// project.
	services := make([]string, 0, len(m.Services))
	apps := make([]ProjectApp, 0, len(m.Services))
	for name, svc := range m.Services {
		sname := sanitizeName(name)
		peerManifest := m
		peerManifest.Policies.Peers = servicePeerNames(m, svc)
		servicePeers, policyAddonPeers := projectPeers(peerManifest, addonResults)
		// The defaults are the ones of the app registration: the manifest
		// and the API must not disagree about a port or a probe.
		infisicalProjectID, infisicalAuthRef, infisicalPath := "", "", ""
		if m.Infisical != nil {
			infisicalProjectID = strings.TrimSpace(m.Infisical.ProjectID)
			infisicalAuthRef = strings.TrimSpace(m.Infisical.AuthRef)
			infisicalPath = strings.TrimSpace(m.Infisical.Path)
		}
		spec, err := normalizeSpec(AppSpec{
			Name:               sname,
			Hostname:           svc.Hostname,
			Port:               svc.Port,
			Healthcheck:        svc.Healthcheck,
			Replicas:           svc.Replicas,
			Internet:           net,
			Peers:              servicePeers,
			Env:                nil,
			Secrets:            svc.Secrets,
			SecretEnv:          svc.SecretEnv,
			Project:            project,
			InfisicalProjectID: infisicalProjectID,
			InfisicalAuthRef:   infisicalAuthRef,
			InfisicalPath:      infisicalPath,
			Namespace:          namespace,
			NetworkPolicy:      &isolated,
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("service %q: %w", name, err)
		}
		spec.StrictPeers = true
		spec.IncomingPeers = incomingServicePeers(m, name)
		for _, peer := range servicePeers {
			port := m.Services[peer].Port
			if port == 0 {
				port = 8080
			}
			spec.ServicePeers = append(spec.ServicePeers, ServicePeer{Name: peer, Port: port})
		}
		// The addons the service uses give it their environment and their
		// network peer. The environment of the addon comes first: what the
		// manifest declares wins.
		used, err := serviceAddons(svc, project, addonResults)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("service %q: %w", name, err)
		}
		spec.Env = append(used.env, serviceEnv(svc)...)
		spec.Command = svc.Command
		spec.AddonPeers = mergeAddonPeers(policyAddonPeers, used.peers)
		// The volumes the addons mount: they come before the volumes the
		// manifest declares, so that a volume of the application is never
		// shadowed by a volume of an addon.
		spec.Volumes = append(used.volumes, svc.Volumes...)
		// A service of a project must not collide with a legacy standalone
		// app: both would claim the same hostname, and Flux would keep
		// deploying the old one. The directory of the project itself is not
		// a standalone app: a platform that keeps the two directories
		// together would otherwise refuse its own projects.
		legacyDir := filepath.Join(work, filepath.FromSlash(p.AppDir(sname)))
		projectDir := filepath.Join(work, filepath.FromSlash(p.ProjectDir(project)))
		if legacyDir != projectDir {
			if _, err := os.Stat(legacyDir); err == nil {
				return nil, nil, nil, fmt.Errorf(
					"service %q: the standalone app %q still exists in the GitOps: remove it before declaring the service in a project",
					name, sname)
			}
		}
		spec.ConfigurationRevision = specHash(m)
		spec.TLSSecretName = sname + "-tls"
		// A service that runs a published image is not built.
		spec.Image = strings.TrimSpace(svc.Image)
		if spec.Image != "" {
			// The image is pinned by digest. The manifest keeps the tag:
			// the manifest is the source of truth and is never rewritten.
			// The digest lives in the generated file, like the digest of
			// a built image.
			digest, err := oci.ResolveDigestWithPolicy(ctx, spec.Image, p.Egress)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("service %q: the image %q could not be pinned: %w", name, spec.Image, err)
			}
			// The digest is pinned with the repository: a digest alone
			// is not an image the cluster can pull.
			spec.Image = oci.Pin(spec.Image, digest)
		}
		spec.WritableRoot = svc.WritableRoot
		spec.RunAsRoot = svc.RunAsRoot
		spec.Capabilities = svc.Capabilities
		spec.Resources = svc.Resources
		spec.StartupBudget = svc.StartupBudget
		dir := filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, sname)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, nil, err
		}
		// The files of a service are generated from the manifest: the
		// manifest is the source of truth of the runtime. The values file
		// keeps the image and the track pinned by the control plane, they
		// are the only two fields Kuberpack does not own there.
		if err := writeServiceValues(filepath.Join(dir, "values.yaml"), spec, p); err != nil {
			return nil, nil, nil, err
		}
		if err := writeFile(filepath.Join(dir, "helmrelease.yaml"), RenderHelmRelease(spec, p)); err != nil {
			return nil, nil, nil, err
		}
		if err := writeFile(filepath.Join(dir, "kustomization.yaml"), RenderAppKustomization(spec, p)); err != nil {
			return nil, nil, nil, err
		}
		// A service that declares secrets gets an InfisicalStaticSecret:
		// the operator synchronises the values from Infisical into a
		// Secret of the namespace. The values never transit by Kuberpack.
		// Project secrets travel with the project; they do not depend on a
		// separately maintained root kustomization. Remove the legacy copy.
		if _, _, err := removeSecretCR(work, spec, p); err != nil {
			return nil, nil, nil, err
		}
		// A volume with a size is a disk: the claim is written with the
		// service, so the data outlives the pod. A volume that comes from
		// an addon has its claim already: the addon wrote it.
		for _, volume := range spec.Volumes {
			if volume.Size == "" || volume.Claim != "" {
				continue
			}
			if err := writeFile(filepath.Join(dir, volume.Name+".pvc.yaml"), RenderVolumeClaim(volume, spec, p)); err != nil {
				return nil, nil, nil, err
			}
		}
		if rbac := RenderRegistryRBAC(spec, p); rbac != "" {
			if err := writeFile(filepath.Join(dir, "rbac.yaml"), rbac); err != nil {
				return nil, nil, nil, err
			}
		}
		if err := writeFile(filepath.Join(dir, "status-rbac.yaml"), RenderStatusRBAC(spec, p)); err != nil {
			return nil, nil, nil, err
		}
		if certificate := RenderCertificate(spec, p); certificate != "" {
			if err := writeFile(filepath.Join(dir, "certificate.yaml"), certificate); err != nil {
				return nil, nil, nil, err
			}
		}
		if len(svc.Secrets) > 0 && p.InfisicalEnabled() {
			if err := writeFile(filepath.Join(dir, "secret.yaml"), RenderSecretCR(spec, p)); err != nil {
				return nil, nil, nil, err
			}
		} else if err := os.Remove(filepath.Join(dir, "secret.yaml")); err != nil && !os.IsNotExist(err) {
			return nil, nil, nil, err
		}
		services = append(services, sname)
		app := projectApp(p, project, sname, svc, namespace, net, servicePeers, m.Infisical)
		app.Image = spec.Image
		apps = append(apps, app)
	}
	sort.Strings(services)
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })

	// A service whose image is not pinned yet is not activated: Flux would
	// otherwise install a HelmRelease whose chart requires a digest.
	activated := make([]string, 0, len(services))
	for _, sname := range services {
		if serviceHasPinnedImage(filepath.Join(work, filepath.FromSlash(p.projectServiceDir(project, sname)))) {
			activated = append(activated, sname)
		}
	}
	// Observation must work before the first source build has an image. Its
	// read-only roles are applied even when the service release is not active.
	resources := make([]string, 0, len(services)+len(activated)+len(addons))
	for _, service := range services {
		resources = append(resources, service+"/status-rbac.yaml")
	}
	resources = append(resources, activated...)
	resources = append(resources, addons...)
	// The namespace of the project is created by the project itself in the
	// historical layout. With tenant isolation, the control plane creates it
	// before Flux: the per-project ServiceAccount cannot create a Namespace.
	prefix := []string{"networkpolicy.yaml"}
	if p.TenantIsolation {
		namespacePath := filepath.Join(projectDir, "namespace.yaml")
		if err := os.Remove(namespacePath); err != nil && !os.IsNotExist(err) {
			return nil, nil, nil, err
		}
	} else {
		if err := writeFile(filepath.Join(projectDir, "namespace.yaml"), RenderProjectNamespace(namespace, project)); err != nil {
			return nil, nil, nil, err
		}
		prefix = []string{"namespace.yaml", "networkpolicy.yaml"}
	}
	if err := writeFile(filepath.Join(projectDir, "networkpolicy.yaml"), RenderProjectDefaultDeny(namespace)); err != nil {
		return nil, nil, nil, err
	}
	parentYAML := RenderParentKustomization(namespace, append(prefix, resources...))
	if err := os.WriteFile(filepath.Join(projectDir, "kustomization.yaml"), []byte(parentYAML), 0o644); err != nil {
		return nil, nil, nil, err
	}

	// The projects parent lists the project: Flux only reconciles a project
	// once it is referenced there.
	parentPath := filepath.Join(work, filepath.FromSlash(p.ProjectsParentPath()))
	parentRaw, err := os.ReadFile(parentPath)
	if os.IsNotExist(err) {
		parentRaw = []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	} else if err != nil {
		return nil, nil, nil, err
	}
	if !listedInKustomization(parentRaw, project) {
		parentRaw = appendKustomizationResource(parentRaw, project)
	}
	if err := os.WriteFile(parentPath, parentRaw, 0o644); err != nil {
		return nil, nil, nil, err
	}
	return services, addons, apps, nil
}

// projectApp is the operational description of a service: what to build and
// where the digest must land. The defaults are the ones of the app
// registration: the manifest and the API must not disagree.
func projectApp(p Platform, project, service string, svc Service, namespace string, internet *bool, peers []string, infisical *InfisicalScope) ProjectApp {
	port := svc.Port
	if port == 0 {
		port = 8080
	}
	healthcheck := strings.TrimSpace(svc.Healthcheck)
	if healthcheck == "" {
		healthcheck = "/"
	}
	branch := strings.TrimSpace(svc.Branch)
	if branch == "" {
		branch = "main"
	}
	strategyName := strings.TrimSpace(svc.Strategy)
	if strategyName == "" {
		strategyName = "auto"
	}
	secretEnv := strings.TrimSpace(svc.SecretEnv)
	if secretEnv == "" {
		secretEnv = "prod"
	}
	app := ProjectApp{
		// The name of an app is global: two projects may declare a service
		// with the same name.
		Name:        sanitizeName(project + "-" + service),
		Service:     service,
		Repository:  strings.ToLower(strings.TrimSpace(svc.Repository)),
		Branch:      branch,
		Strategy:    strategyName,
		Hostname:    strings.TrimSpace(svc.Hostname),
		Port:        port,
		Healthcheck: healthcheck,
		Secrets:     normalizeSecretKeys(svc.Secrets),
		SecretEnv:   secretEnv,
		Internet:    internet,
		Peers:       peers,
		Namespace:   namespace,
		// Where the digest of the build lands: the values file of the
		// service, inside the project.
		ValuesPath: p.ProjectServiceValuesPath(project, service),
	}
	if infisical != nil {
		app.InfisicalProjectID = strings.TrimSpace(infisical.ProjectID)
		app.InfisicalAuthRef = strings.TrimSpace(infisical.AuthRef)
		app.InfisicalPath = strings.TrimSpace(infisical.Path)
	}
	if svc.Build != nil {
		app.RootDirectory = strings.TrimSpace(svc.Build.RootDirectory)
		app.BuildCommand = strings.TrimSpace(svc.Build.BuildCommand)
		app.StartCommand = strings.TrimSpace(svc.Build.StartCommand)
	}
	return app
}

// writeServiceValues writes the values file of a service from the manifest.
//
// The image of a published application belongs to the manifest: the
// compiler resolves its tag and pins it. The image of a built application
// belongs to the control plane: it is the digest of the build, and the
// compiler must not touch it. The track belongs to the control plane too.
func writeServiceValues(path string, spec AppSpec, p Platform) error {
	content := []byte(valuesYAML(spec, p))
	raw, err := os.ReadFile(path)
	if err == nil {
		image, track := pinnedValues(raw)
		if image != "" && strings.TrimSpace(spec.Image) == "" {
			patched, perr := PatchImage(content, image)
			if perr != nil {
				return perr
			}
			content = patched
		}
		if track != "" && track != "main" {
			patched, perr := PatchTrack(content, track)
			if perr != nil {
				return perr
			}
			content = patched
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// projectNamespace is the namespace the resources of a project land in. It
// defaults to the name of the project.
// ProjectNamespace exposes the namespace a manifest wants, for the
// preflight ownership check of the control plane.
func ProjectNamespace(m ProjectManifest, project string) string {
	return projectNamespace(m, project)
}

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
func RenderProjectNamespace(namespace, project string) string {
	return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + namespace +
		"\n  labels:\n    app.kubernetes.io/part-of: kuberpack\n    kuberpack.io/project: " + project +
		"\n    pod-security.kubernetes.io/enforce: baseline\n    pod-security.kubernetes.io/audit: restricted\n    pod-security.kubernetes.io/warn: restricted\n"
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

// compileAddons renders the addons of a project. The engine of each addon
// is looked up in the registry: an unknown engine is refused with the list
// of the available ones.
func compileAddons(work string, m ProjectManifest, project string, namespace string, p Platform) ([]string, map[string]AddonResult, error) {
	results := map[string]AddonResult{}
	names := make([]string, 0, len(m.Addons))
	for name, addon := range m.Addons {
		engine, ok := AddonEngineFor(addon.Engine)
		if !ok {
			return nil, nil, fmt.Errorf("addon %q: engine %q is not available (%s)",
				name, addon.Engine, strings.Join(AddonEngineNames(), ", "))
		}
		// The type writes the resources of an operator: without it, the
		// whole project fails to apply, not only the database.
		if operator := addonOperator(addon.Engine); operator != "" && !hasOperator(p, operator) {
			return nil, nil, fmt.Errorf(
				"addon %q: the type %q needs the operator %q, which the platform does not declare (it declares: %s)",
				name, addon.Engine, operator, strings.Join(p.AddonOperators, ", "))
		}
		aname := sanitizeName(name)
		dir := filepath.Join(work, filepath.FromSlash(p.projectAddonDir(project, aname)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		siblings := map[string]string{}
		for other, declared := range m.Addons {
			if other == name {
				continue
			}
			entry, known := AddonCatalogEntryForEngine(declared.Engine)
			if !known {
				continue
			}
			siblings[sanitizeName(other)] = entry.Kind
		}
		result, err := engine.Render(dir, AddonSpec{
			Name:           aname,
			Project:        project,
			Namespace:      namespace,
			Database:       addon.Database,
			Owner:          addon.Owner,
			Backup:         addon.Backup,
			Plan:           addon.Plan,
			Params:         addon.Params,
			Version:        addon.Version,
			Path:           addon.Path,
			Siblings:       siblings,
			ServiceClients: addonServiceClients(m, name),
			AddonClients:   addonDependencyClients(m, name),
		}, p)
		if err != nil {
			return nil, nil, fmt.Errorf("addon %q: %w", name, err)
		}
		results[aname] = result
		// The parent kustomization lists the addons by their path in the
		// project, not by their name.
		names = append(names, "addons/"+aname)
	}
	sort.Strings(names)
	return names, results, nil
}

// serviceAddons returns what the addons a service uses give to it: their
// environment and their network peer.
func serviceAddons(svc Service, project string, results map[string]AddonResult) (struct {
	env     []EnvVar
	peers   []AddonPeer
	volumes []Volume
}, error) {
	var out struct {
		env     []EnvVar
		peers   []AddonPeer
		volumes []Volume
	}
	seen := map[string]bool{}
	for _, name := range svc.Addons {
		aname := sanitizeName(name)
		result, ok := results[aname]
		if !ok {
			return out, fmt.Errorf("addon %q is not declared in the project", name)
		}
		if seen[aname] {
			continue
		}
		seen[aname] = true
		for _, env := range result.Env {
			out.env = append(out.env, EnvVar{
				Name:       env.Name,
				Value:      env.Value,
				SecretName: env.SecretName,
				SecretKey:  env.SecretKey,
			})
		}
		if result.Kind != "" {
			out.peers = append(out.peers, AddonPeer{
				Kind: result.Kind,
				Name: aname,
				For:  sanitizeName(project),
				Port: result.Port,
			})
		}
		// The volumes the addon mounts: a database that is a file is
		// a disk, and the application needs it.
		for _, mount := range result.Mounts {
			out.volumes = append(out.volumes, Volume{
				Name:  mount.Name,
				Path:  mount.Path,
				Size:  mount.Size,
				Claim: mount.Claim,
			})
		}
	}
	return out, nil
}

// mergeAddonPeers merges the peers of the addons, without duplicates: a
// service that uses an addon named in the policy must not have it twice.
func mergeAddonPeers(lists ...[]AddonPeer) []AddonPeer {
	seen := map[string]bool{}
	var out []AddonPeer
	for _, list := range lists {
		for _, peer := range list {
			key := peer.Kind + "/" + peer.Name + "/" + peer.For
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, peer)
		}
	}
	return out
}

// projectPeers splits the policy peers: the services of the project and the
// addons of the project. A peer that is neither is refused by the
// validation of the manifest.
func projectPeers(m ProjectManifest, results map[string]AddonResult) ([]string, []AddonPeer) {
	project := sanitizeName(m.Project)
	var services []string
	var addons []AddonPeer
	for _, peer := range m.Policies.Peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		if result, ok := results[sanitizeName(peer)]; ok {
			if result.Kind != "" {
				addons = append(addons, AddonPeer{
					Kind: result.Kind,
					Name: sanitizeName(peer),
					For:  project,
					Port: result.Port,
				})
			}
			continue
		}
		services = append(services, sanitizeName(peer))
	}
	return services, addons
}

// projectAddonsOf lists the addons a project declares, read from its
// manifest in the repository.
func projectAddonsOf(work string, p Platform, project string) ([]string, error) {
	path := filepath.Join(work, filepath.FromSlash(p.ProjectManifestPath(project)))
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	manifest, err := ParseProjectManifest(raw)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(manifest.Addons))
	for name := range manifest.Addons {
		out = append(out, sanitizeName(name))
	}
	sort.Strings(out)
	return out, nil
}

// addonOperator returns the operator the type of an engine needs.
func addonOperator(engine string) string {
	entry, ok := AddonCatalogEntryForEngine(engine)
	if !ok {
		return ""
	}
	return strings.TrimSpace(entry.Operator)
}

// hasOperator reports whether the platform declares an operator.
func hasOperator(p Platform, operator string) bool {
	for _, declared := range p.AddonOperators {
		if strings.EqualFold(strings.TrimSpace(declared), operator) {
			return true
		}
	}
	return false
}

// writeFile writes a generated file. The platform owns these files: they
// are rewritten when the platform changes, so that a change of the platform
// reaches the applications that were compiled before it.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
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
	if err := guardProjectTree(work, req.Platform); err != nil {
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
	if err := guardProjectTree(work, req.Platform); err != nil {
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
	if err := guardProjectTree(work, req.Platform); err != nil {
		return ProjectResult{}, err
	}
	rel := req.Platform.ProjectDir(project)
	// A project that holds a database is not removed silently: the data
	// leaves the cluster first, with the Job the engine wrote.
	if err := checkExpectedManifest(work, req); err != nil {
		return ProjectResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(req.Platform.ProjectManifestPath(project))))
	if os.IsNotExist(err) {
		return ProjectResult{Project: project}, nil
	}
	if err != nil {
		return ProjectResult{}, fmt.Errorf("cannot inventory project before deletion: %w", err)
	}
	m, err := ParseProjectManifest(raw)
	if err != nil {
		return ProjectResult{}, err
	}
	if len(m.Addons) > 0 {
		names := make([]string, 0, len(m.Addons))
		for name := range m.Addons {
			names = append(names, name)
		}
		sort.Strings(names)
		return ProjectResult{}, fmt.Errorf("project declares addons %s: explicit data decommissioning is required", strings.Join(names, ", "))
	}
	for _, v := range m.Services {
		for _, vol := range v.Volumes {
			if vol.Size != "" || vol.Claim != "" {
				return ProjectResult{}, fmt.Errorf("project contains persistent service volumes")
			}
		}
	}

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
	if err := SyncInfisicalOperatorScope(work, req.Platform); err != nil {
		return ProjectResult{}, err
	}
	if req.Platform.InfisicalOperatorScope {
		add = append(add, filepath.ToSlash(filepath.Join(req.Platform.ProjectsDirPath(), InfisicalOperatorScopeFile)))
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

func specHash(m ProjectManifest) string { return spec.Hash(m) }

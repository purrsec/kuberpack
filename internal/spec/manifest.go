package spec

// ProjectManifest is the human-authored source compiled into GitOps primitives.
type ProjectManifest struct {
	Project   string             `yaml:"project" json:"project"`
	Namespace string             `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Services  map[string]Service `yaml:"services,omitempty" json:"services,omitempty"`
	Addons    map[string]Addon   `yaml:"addons,omitempty" json:"addons,omitempty"`
	Policies  Policy             `yaml:"policies,omitempty" json:"policies,omitempty"`
	// Infisical optionally gives this project its own Infisical project,
	// identity and secret path. Without it, the shared platform project is
	// used for compatibility. The administrator creates the identity and
	// grants the operator access; the compiler only references it.
	Infisical *InfisicalScope `yaml:"infisical,omitempty" json:"infisical,omitempty"`
}

// InfisicalScope is the per-project secret boundary.
type InfisicalScope struct {
	ProjectID string `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	AuthRef   string `yaml:"auth_ref,omitempty" json:"auth_ref,omitempty"`
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`
}

// Service is one application of a project.
type Service struct {
	Type string `yaml:"type,omitempty" json:"type,omitempty"` // stateless
	// Repository is the Forgejo repository the image is built from. It is
	// the entry point of the build pipeline: a service without one is never
	// built.
	Repository string `yaml:"repository,omitempty" json:"repository,omitempty"`
	// Image runs a published image instead of building one. It is how a
	// third party application is deployed: the platform never writes in
	// the repository of somebody else, and it never builds what its
	// authors publish.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Branch is the branch whose pushes become the production. Defaults to
	// the default branch of the repository.
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// Strategy is auto, uv or railpack.
	Strategy    string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Port        int      `yaml:"port,omitempty" json:"port,omitempty"`
	Hostname    string   `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Healthcheck string   `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Replicas    *int     `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Secrets     []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	SecretEnv   string   `yaml:"secret_env,omitempty" json:"secret_env,omitempty"`
	// Env are literal environment variables of the application.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Command is what the container runs, when the image expects
	// something else than its default. Many published images want a
	// command: ntfy wants "serve", for a third party application the
	// platform does not build.
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// Addons are the addons of the project the service uses: it gets their
	// environment and may reach them.
	Addons []string `yaml:"addons,omitempty" json:"addons,omitempty"`
	// Peers are outgoing neighbours inside this project. Nil inherits the
	// project policy for compatibility; an explicit [] grants no neighbours.
	Peers *[]string `yaml:"peers,omitempty" json:"peers,omitempty"`
	// Build holds the build-time overrides of the service.
	Build *BuildOptions `yaml:"build,omitempty" json:"build,omitempty"`
	// Volumes are the volumes of the service. A volume without a size is
	// temporary; a volume with a size is a disk that outlives the pod.
	// It is how a database that is a file is deployed.
	Volumes []Volume `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	// WritableRoot lets the service write in its own filesystem. A
	// published image sometimes expects it.
	WritableRoot bool `yaml:"writable_root,omitempty" json:"writable_root,omitempty"`
	// RunAsRoot lets the service run as root. A published image with an
	// init system sometimes expects it.
	RunAsRoot bool `yaml:"run_as_root,omitempty" json:"run_as_root,omitempty"`
	// Capabilities are the capabilities the service needs. The platform
	// drops them all; an image with an init system needs to change the
	// owner of its files and to drop its privileges itself.
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// Resources are the cpu and the memory of the service. Without them,
	// the platform's defaults apply.
	Resources *ServiceResources `yaml:"resources,omitempty" json:"resources,omitempty"`
	// StartupBudget is how long the application may take to start, in
	// seconds. An application that is killed while it starts never
	// starts: the budget is the difference between a slow start and a
	// crash loop.
	StartupBudget int `yaml:"startup_budget,omitempty" json:"startup_budget,omitempty"`
}

// ServiceResources are the cpu and the memory of a service.
type ServiceResources struct {
	Requests ResourceList `yaml:"requests,omitempty" json:"requests,omitempty"`
	Limits   ResourceList `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// Volume is a volume of a service.
type Volume struct {
	// Name is the name of the volume.
	Name string `yaml:"name" json:"name"`
	// Path is where the volume is mounted.
	Path string `yaml:"path" json:"path"`
	// Size is the size of the disk. Empty means a temporary volume.
	Size string `yaml:"size,omitempty" json:"size,omitempty"`
	// Claim is the name of the PersistentVolumeClaim the volume comes
	// from. It is empty when the claim is named after the application.
	Claim string `yaml:"claim,omitempty" json:"claim,omitempty"`
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
	// Plan is the size of the addon: "small" or "medium". The sizes are
	// the ones of the catalogue, the same everywhere.
	Plan string `yaml:"plan,omitempty" json:"plan,omitempty"`
	// Version is the version of the addon: the major version of the
	// database, for a database. It is the one of the catalogue, unless
	// the catalogue holds several.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Path is where the addon mounts its volume, when it has one.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Params overrides what the plan holds: instances, storage, cpu,
	// memory.
	Params map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
}

// Backup describes how an addon is backed up.
type Backup struct {
	// S3 names the S3-compatible addon of this project used for backups.
	S3 string `yaml:"s3,omitempty" json:"s3,omitempty"`
	// Retention is how long the backups are kept (30d, 7d...).
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// Policy is the network policy of the project.
type Policy struct {
	// DefaultDeny isolates the services of the project. It defaults to true:
	// a service only reaches what the manifest allows.
	DefaultDeny *bool    `yaml:"defaultDeny,omitempty" json:"defaultDeny,omitempty"`
	Internet    *bool    `yaml:"internet,omitempty" json:"internet,omitempty"`
	Peers       []string `yaml:"peers,omitempty" json:"peers,omitempty"`
}

// defaultDenyEnabled reports whether the project isolates its services.
func (p Policy) DefaultDenyEnabled() bool {
	if p.DefaultDeny == nil {
		return true
	}
	return *p.DefaultDeny
}

// BoolPtr returns a pointer to a bool, for the optional fields of a manifest.
func BoolPtr(v bool) *bool { return &v }

type ResourceList struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

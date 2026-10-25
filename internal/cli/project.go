package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type initOptions struct {
	file, repository, image, hostname, branch string
	healthcheck, cpu, memory                  string
	addons                                    []string
	port                                      int
	fromRemote                                bool
}

func (a *App) initCommand() *cobra.Command {
	o := initOptions{file: "kuberpack.yaml", healthcheck: "/"}
	c := confirms(&cobra.Command{Use: "init", Short: "Prepare a local manifest with a guided form", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.initProject(o) }})
	c.Flags().StringVar(&o.file, "file", o.file, "Manifest destination (never overwritten)")
	c.Flags().StringVar(&o.repository, "repository", "", "Source repository (otherwise proposed from Git)")
	c.Flags().StringVar(&o.image, "image", "", "Published image instead of a source repository")
	c.Flags().StringVar(&o.hostname, "hostname", "", "Public hostname")
	c.Flags().StringVar(&o.branch, "branch", "", "Production branch (otherwise repository default)")
	c.Flags().IntVar(&o.port, "port", 0, "Application port")
	c.Flags().StringVar(&o.healthcheck, "healthcheck", "/", "HTTP healthcheck path")
	c.Flags().StringVar(&o.cpu, "cpu-limit", "", "CPU limit (for example 500m)")
	c.Flags().StringVar(&o.memory, "memory-limit", "", "Memory limit (for example 512Mi)")
	c.Flags().StringSliceVar(&o.addons, "addon", nil, "Optional dependency: postgres or valkey (repeatable)")

	c.Flags().BoolVar(&o.fromRemote, "from-remote", false, "Explicitly retrieve the complete remote manifest")
	return c
}
func (a *App) manifestPath(file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	return filepath.Join(a.WorkDir, file)
}
func (a *App) projectTarget(c *client, session api.Session, fromManifest string) (string, error) {
	project := a.project
	if project == "" {
		_, link, ok := a.directoryLink()
		if ok {
			if a.contextName != "" && a.contextName != link.Context {
				return "", fail(2, "changing a linked context requires --project or a new link")
			}
			project = link.Project
		}
	}
	if project == "" {
		project = fromManifest
	}
	if project == "" {
		project = session.Project
	}
	if project == "" {
		var out struct {
			Projects []api.Project `json:"projects"`
		}
		if err := c.json(a.ctx, http.MethodGet, "/api/v2/projects", nil, &out); err != nil {
			return "", err
		}
		names := []string{}
		for _, p := range out.Projects {
			names = append(names, p.Name)
		}
		var err error
		project, err = a.selectOne("project", names)
		if err != nil {
			return "", err
		}
	}
	if !spec.NamePattern.MatchString(project) {
		return "", fail(2, "invalid project name")
	}
	if session.Project != "" && session.Project != project {
		return "", fail(4, "credential is scoped to project %s", session.Project)
	}
	if fromManifest != "" && project != fromManifest {
		return "", fail(2, "manifest project does not match the selected project")
	}
	return project, nil
}
func (a *App) getProject(c *client, project string) (api.Project, error) {
	var p api.Project
	err := c.json(a.ctx, http.MethodGet, "/api/v2/projects/"+url.PathEscape(project), nil, &p)
	return p, err
}
func (a *App) initProject(o initOptions) error {
	file := a.manifestPath(o.file)
	if _, err := os.Lstat(file); err == nil {
		return fail(2, "%s already exists; it was preserved", file)
	} else if !os.IsNotExist(err) {
		return err
	}
	project := a.project
	revision := ""
	verified := false
	var remote api.Project
	if a.hasConnection() {
		c, session, err := a.connect()
		if err != nil {
			return err
		}
		project, err = a.projectTarget(c, session, "")
		if err != nil {
			return err
		}
		remote, err = a.getProject(c, project)
		if err != nil {
			return err
		}
		revision = remote.Revision
		verified = true
		var old spec.ProjectManifest
		if remote.Manifest != "" {
			old, err = spec.Parse([]byte(remote.Manifest))
			if err != nil {
				return err
			}
		}
		if len(old.Services) > 0 || len(old.Addons) > 0 {
			if !o.fromRemote {
				if !a.interactive() {
					return fail(2, "project is not empty; use link or explicitly retrieve its complete manifest with init --from-remote")
				}
				choice, err := a.selectOne("Existing project", []string{"Associate this folder only", "Retrieve the complete manifest for editing"})
				if err != nil {
					return err
				}
				if strings.HasPrefix(choice, "Associate") {
					return a.linkTo(c, session, project)
				}
			}
			if remote.Manifest == "" {
				return fail(4, "this credential cannot retrieve the complete project manifest")
			}
			if err := a.confirm("Save the complete remote manifest locally?"); err != nil {
				return err
			}
			if err := exclusiveFile(file, []byte(remote.Manifest), 0644); err != nil {
				return err
			}
			a.config.Links[a.WorkDir] = association{Context: a.resolvedContext, Project: project, Manifest: file, Revision: revision, Prepared: true}
			if a.resolvedContext != "" {
				if err := a.saveConfig(); err != nil {
					return err
				}
			}
			return a.print(map[string]string{"manifest": file, "project": project, "status": "retrieved_complete_manifest"})
		}
	}
	if project == "" {
		var err error
		project, err = a.ask("Project name (--project)", "")
		if err != nil {
			return err
		}
	}
	if !spec.NamePattern.MatchString(project) {
		return fail(2, "invalid project name")
	}
	service := a.service
	if service == "" {
		service = "web"
	}
	if o.repository != "" && o.image != "" {
		return fail(2, "choose --repository or --image")
	}
	if o.repository == "" && o.image == "" {
		o.repository = a.gitRepository()
		if o.repository == "" {
			if !a.interactive() {
				return fail(2, "provide --repository or --image")
			}
			choice, err := a.selectOne("Source", []string{"Git repository", "Published image"})
			if err != nil {
				return err
			}
			if choice == "Published image" {
				o.image, err = a.ask("Published image reference", "")
			} else {
				o.repository, err = a.ask("Source repository", "")
			}
			if err != nil {
				return err
			}
		}
	}
	if o.port == 0 {
		v, err := a.ask("Application port (--port)", "8080")
		if err != nil {
			return err
		}
		o.port, err = strconv.Atoi(v)
		if err != nil {
			return fail(2, "invalid port")
		}
	}
	if o.hostname == "" {
		var err error
		o.hostname, err = a.ask("Public hostname (--hostname)", "")
		if err != nil {
			return err
		}
	}
	for {
		fmt.Fprintf(a.Err, "\nProject  %s\nService  %s\nSource   %s%s\nPort     %d\nHostname %s\n", clean(project, a.secret), clean(service, a.secret), clean(o.repository, a.secret), clean(o.image, a.secret), o.port, clean(o.hostname, a.secret))
		if a.yes {
			break
		}
		if !a.interactive() {
			if !a.yes {
				return fail(2, "--yes is required to save the local manifest")
			}
			break
		}
		choice, err := a.selectOne("Configuration", []string{"Save local configuration", "Edit the proposed values", "Advanced settings", "Cancel"})
		if err != nil {
			return err
		}
		switch choice {
		case "Save local configuration":
			goto save
		case "Cancel":
			return fail(2, "cancelled; no manifest was written")
		case "Edit the proposed values":
			service, err = a.ask("Service", service)
			if err != nil {
				return err
			}
			kind, e := a.selectOne("Source type", []string{"Git repository", "Published image"})
			if e != nil {
				return e
			}
			if kind == "Git repository" {
				o.repository, err = a.ask("Repository", o.repository)
				o.image = ""
			} else {
				o.image, err = a.ask("Image", o.image)
				o.repository = ""
				o.branch = ""
			}
			if err != nil {
				return err
			}

			o.hostname, err = a.ask("Hostname", o.hostname)
			if err != nil {
				return err
			}
		case "Advanced settings":
			if err := a.advancedInit(&o); err != nil {
				return err
			}

		}
	}
save:
	m := spec.ProjectManifest{Project: project, Services: map[string]spec.Service{service: {Repository: o.repository, Image: o.image, Branch: o.branch, Hostname: o.hostname, Port: o.port, Healthcheck: "/"}}}
	v := m.Services[service]
	if o.healthcheck != "" {
		v.Healthcheck = o.healthcheck
	}
	if o.cpu != "" || o.memory != "" {
		v.Resources = &spec.ServiceResources{Limits: spec.ResourceList{CPU: o.cpu, Memory: o.memory}}
	}
	if len(o.addons) > 0 {
		m.Addons = map[string]spec.Addon{}
		for _, name := range o.addons {
			switch name {
			case "postgres":
				m.Addons[name] = spec.Addon{Engine: "cnpg", Database: project, Owner: project}
			case "valkey":
				m.Addons[name] = spec.Addon{Engine: "valkey", Plan: "small"}
			default:
				return fail(2, "unsupported --addon %s; choose postgres or valkey", name)
			}
			v.Addons = append(v.Addons, name)
		}
	}
	m.Services[service] = v
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := spec.Parse(b); err != nil {
		return fail(2, "%s", err)
	}
	if err := exclusiveFile(file, b, 0644); err != nil {
		return err
	}
	// Only a verified reserved project is associated; a new service remains a
	// local draft until it is observed in the API.
	if verified {
		a.config.Links[a.WorkDir] = association{Context: a.resolvedContext, Project: project, Manifest: file, Revision: revision, Prepared: true}
		if a.resolvedContext != "" {
			if err := a.saveConfig(); err != nil {
				return err
			}
		}
	}
	return a.print(map[string]any{"manifest": file, "project": project, "remote_project_verified": verified, "status": "prepared_locally"})
}
func (a *App) hasConnection() bool {
	if os.Getenv("KUBERPACK_API_URL") != "" || os.Getenv("KUBERPACK_API_TOKEN") != "" {
		return true
	}
	if a.contextName != "" {
		_, ok := a.config.Contexts[a.contextName]
		return ok
	}
	if _, _, ok := a.directoryLink(); ok {
		return true
	}
	_, ok := a.config.Contexts[a.config.Current]
	return ok
}
func (a *App) ensureConnection() error {
	if a.hasConnection() {
		return nil
	}
	if !a.interactive() {
		return fail(2, "no connection; run login")
	}
	return a.login(loginOptions{store: "keyring"})
}
func (a *App) gitRepository() string {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = a.WorkDir
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(b))
	var host, path string
	if strings.Contains(raw, "@") && !strings.Contains(raw, "://") {
		left, right, ok := strings.Cut(raw, ":")
		if !ok {
			return ""
		}
		_, host, _ = strings.Cut(left, "@")
		path = right
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return ""
		}
		host = u.Host
		path = u.Path
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return ""
	}
	configured, err := url.Parse(a.forgeURL)
	if err == nil && strings.EqualFold(configured.Host, host) {
		return path
	}
	return "https://" + host + "/" + path + ".git"

}

func (a *App) linkCommand() *cobra.Command {
	return confirms(&cobra.Command{Use: "link", Short: "Associate this directory with an existing project", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if err := a.ensureConnection(); err != nil {
			return err
		}
		c, session, err := a.connect()
		if err != nil {
			return err
		}
		project, err := a.projectTarget(c, session, "")
		if err != nil {
			return err
		}
		return a.linkTo(c, session, project)
	}})
}
func (a *App) linkTo(c *client, session api.Session, project string) error {
	p, err := a.getProject(c, project)
	if err != nil {
		return err
	}
	service := a.service
	if service == "" && len(p.Services) > 0 {
		names := []string{}
		for _, s := range p.Services {
			names = append(names, s.Name)
		}
		sort.Strings(names)
		service, err = a.selectOne("service", names)
		if err != nil {
			return err
		}
	}
	if service != "" {
		found := false
		for _, s := range p.Services {
			if s.Name == service {
				found = true
			}
		}
		if !found {
			return fail(2, "the selected service is not registered")
		}
	}
	if err := a.confirm("Save this local project association?"); err != nil {
		return err
	}
	a.config.Links[a.WorkDir] = association{Context: a.resolvedContext, Project: project, Service: service, Revision: p.Revision}
	if err := a.saveConfig(); err != nil {
		return err
	}
	return a.print(map[string]string{"project": project, "service": service, "directory": a.WorkDir})
}
func (a *App) unlinkCommand() *cobra.Command {
	return confirms(&cobra.Command{Use: "unlink", Short: "Remove the local association only", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		dir, _, ok := a.directoryLink()
		if !ok {
			return fail(2, "directory is not linked")
		}
		if err := a.confirm("Remove the local association for " + dir + "?"); err != nil {
			return err
		}
		delete(a.config.Links, dir)
		return a.saveConfig()
	}})
}

func (a *App) wizard() error {
	if !a.interactive() {
		return fail(2, "wizard requires an interactive terminal; use login, init, link or deploy with explicit parameters")
	}
	fmt.Fprintf(a.Err, "Kuberpack — %s\n", clean(a.WorkDir, a.secret))
	tasks := []string{"Prepare and deploy this directory", "Rebuild a registered service", "Link an existing project", "View service status", "Read application logs", "Connect to an instance"}
	contextName := a.contextName
	if contextName == "" {
		_, link, ok := a.directoryLink()
		if ok {
			contextName = link.Context
		}
		if contextName == "" {
			contextName = a.config.Current
		}
	}
	role := a.config.Contexts[contextName].Role
	if role == "reader" || role == "project" {
		tasks = tasks[2:]
	} else if role == "deployer" {
		tasks = tasks[1:]
	}
	choice, err := a.selectOne("Task", tasks)
	if err != nil {
		return err
	}
	if choice == "Connect to an instance" {
		return a.login(loginOptions{store: "keyring"})
	}
	if err := a.ensureConnection(); err != nil {
		return err
	}
	switch choice {
	case "Prepare and deploy this directory":
		file := "kuberpack.yaml"
		if _, link, ok := a.directoryLink(); ok && link.Manifest != "" {
			file = link.Manifest
		}
		if _, err := os.Stat(a.manifestPath(file)); os.IsNotExist(err) {
			if err := a.initProject(initOptions{file: file}); err != nil {
				return err
			}
		}
		return a.deploy(deployOptions{file: file}, false)
	case "Rebuild a registered service":
		return a.deploy(deployOptions{}, true)
	case "Link an existing project":
		c, session, err := a.connect()
		if err != nil {
			return err
		}
		p, err := a.projectTarget(c, session, "")
		if err != nil {
			return err
		}
		return a.linkTo(c, session, p)
	case "View service status":
		return a.status(false)
	case "Read application logs":
		return a.logs(logOptions{tail: 100})
	}
	return nil
}

func (a *App) advancedInit(o *initOptions) error {
	choice, err := a.selectOne("Advanced setting", []string{"Branch and port", "Healthcheck", "CPU and memory limits", "Add PostgreSQL", "Add Valkey", "Back"})
	if err != nil {
		return err
	}
	switch choice {
	case "Branch and port":
		branch := o.branch
		if branch == "" {
			branch = "main"
		}
		o.branch, err = a.ask("Production branch", branch)
		if err != nil {
			return err
		}
		value, e := a.ask("Port", strconv.Itoa(o.port))
		if e != nil {
			return e
		}
		o.port, err = strconv.Atoi(value)
		if err != nil {
			return fail(2, "invalid port")
		}
	case "Healthcheck":
		def := o.healthcheck
		if def == "" {
			def = "/"
		}
		o.healthcheck, err = a.ask("Healthcheck path", def)
	case "CPU and memory limits":
		o.cpu, err = a.ask("CPU limit", "500m")
		if err != nil {
			return err
		}
		o.memory, err = a.ask("Memory limit", "512Mi")
	case "Add PostgreSQL":
		if !contains(o.addons, "postgres") {
			o.addons = append(o.addons, "postgres")
		}
	case "Add Valkey":
		if !contains(o.addons, "valkey") {
			o.addons = append(o.addons, "valkey")
		}
	}
	return err
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func apiFailure(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.HasPrefix(r.URL.Path, "/api/v2/") {
		apiError(w, status, strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_")), message)
		return
	}
	http.Error(w, message, status)
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": api.Error{Code: code, Message: message}})
}

func (s *Server) routesV2(m *http.ServeMux) {
	m.HandleFunc("GET /api/v2/session", s.withAPIv2(s.sessionV2))
	m.HandleFunc("GET /api/v2/projects", s.withAPIv2(s.projectsV2))
	m.HandleFunc("GET /api/v2/projects/{project}", s.withAPIv2(s.projectV2))
	m.HandleFunc("GET /api/v2/projects/{project}/services", s.withAPIv2(s.servicesV2))
	m.HandleFunc("POST /api/v2/projects/{project}/plans", s.withAPIv2(s.planV2))
	m.HandleFunc("POST /api/v2/projects/{project}/deployments", s.withAPIv2(s.executeV2))
	m.HandleFunc("GET /api/v2/projects/{project}/deployments", s.withAPIv2(s.deploymentsV2))
	m.HandleFunc("GET /api/v2/projects/{project}/deployments/{id}", s.withAPIv2(s.deploymentV2))
	m.HandleFunc("GET /api/v2/projects/{project}/requests/{id}", s.withAPIv2(s.requestV2))
	m.HandleFunc("POST /api/v2/admin/projects/{project}", s.withAPIv2(s.reserveProjectV2))
	m.HandleFunc("PUT /api/v2/admin/projects/{project}/policy", s.withAPIv2(s.policyV2))
	m.HandleFunc("POST /api/v2/admin/tokens", s.withAPIv2(s.createAPIToken))
	m.HandleFunc("GET /api/v2/admin/tokens", s.withAPIv2(s.listAPITokens))
	m.HandleFunc("DELETE /api/v2/admin/tokens/{id}", s.withAPIv2(s.revokeAPIToken))
}

func (s *Server) withAPIv2(next http.HandlerFunc) http.HandlerFunc {
	return s.withAPI(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Store == nil {
			apiError(w, http.StatusServiceUnavailable, "storage_unavailable", "the API store is unavailable")
			return
		}
		next(w, r)
	})
}

func (s *Server) sessionV2(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	u := ""
	if s.cfg.Provider != nil {
		u = s.cfg.Provider.WebURL()
	}
	writeJSON(w, 200, api.Session{Version: api.Version, TokenID: p.TokenID, Name: p.Name, Role: p.Role, Project: p.Project, Services: p.Services, ExpiresAt: p.ExpiresAt, ForgeURL: u, Capabilities: []string{"plans", "deployments", "idempotency", "scoped_roles", "configuration_observation"}})
}

func (s *Server) requestProject(ctx context.Context, project string) promote.ProjectRequest {
	var auth []string
	if s.cfg.Provider != nil {
		auth = s.cfg.Provider.CloneAuth(s.cfg.Token)
	}
	return promote.ProjectRequest{GitOpsURL: s.cfg.GitOpsURL, GitOpsBranch: s.cfg.GitOpsBranch, HTTPToken: s.cfg.Token, AuthArgs: auth, Manifest: promote.ProjectManifest{Project: project}, Platform: s.cfg.Platform, NamespaceCheck: func(p, n string) error { return s.checkNamespaceOwnership(ctx, p, n) }}
}

func (s *Server) projectManifest(ctx context.Context, project string) ([]byte, promote.ProjectManifest, error) {
	raw, err := promote.ReadProjectManifest(ctx, s.requestProject(ctx, project))
	if err != nil {
		return nil, promote.ProjectManifest{}, err
	}
	m, err := promote.ParseProjectManifest(raw)
	return raw, m, err
}

func (s *Server) projectDTO(ctx context.Context, project string, p Principal) (api.Project, error) {
	raw, m, err := s.projectManifest(ctx, project)
	if errors.Is(err, promote.ErrProjectNotFound) {
		if _, e := s.cfg.Store.Object(ctx, "policy", project); e != nil {
			return api.Project{}, err
		}
		m = promote.ProjectManifest{Project: project}
		err = nil
	}
	if err != nil {
		return api.Project{}, err
	}
	out := api.Project{Name: project, Namespace: promote.ProjectNamespace(m, project), Manifest: string(raw), Services: []api.Service{}}
	if len(raw) > 0 {
		out.Revision = spec.HashBytes(raw)
	}
	names := make([]string, 0, len(m.Services))
	for n := range m.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		v := m.Services[name]
		if len(p.Services) > 0 && !slices.Contains(p.Services, name) {
			continue
		}
		out.Services = append(out.Services, api.Service{ID: project + "-" + name, Name: name, Project: project, Repository: v.Repository, Branch: v.Branch, Image: v.Image, Hostname: v.Hostname})
	}
	// Service-restricted credentials must not receive the complete manifest.
	if len(p.Services) > 0 {
		out.Manifest = ""
	}
	return out, nil
}

func (s *Server) projectAccess(w http.ResponseWriter, r *http.Request) (Principal, string, bool) {
	project := r.PathValue("project")
	if !spec.NamePattern.MatchString(project) {
		apiError(w, 400, "invalid_project", "invalid project name")
		return Principal{}, "", false
	}
	p, ok := requireProject(w, r, project)
	return p, project, ok
}

func (s *Server) projectsV2(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	names := map[string]bool{}
	if p.Project != "" {
		names[p.Project] = true
	} else if p.isAdmin() {
		items, err := promote.ListProjects(r.Context(), s.requestProject(r.Context(), ""))
		if err != nil {
			apiError(w, 502, "gitops_unavailable", "cannot read project inventory")
			return
		}
		for _, x := range items {
			names[x.Project] = true
		}
		objects, err := s.cfg.Store.Objects(r.Context(), "policy", "")
		if err != nil {
			apiError(w, 500, "storage_error", "cannot read reservations")
			return
		}
		for _, o := range objects {
			names[o.Project] = true
		}
	}
	ordered := []string{}
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	out := []api.Project{}
	for _, name := range ordered {
		pr, err := s.projectDTO(r.Context(), name, p)
		if errors.Is(err, promote.ErrProjectNotFound) {
			continue
		}
		if err != nil {
			apiError(w, 502, "gitops_unavailable", "cannot read project "+name)
			return
		}
		pr.Manifest = ""
		out = append(out, pr)
	}
	writeJSON(w, 200, map[string]any{"projects": out})
}
func (s *Server) projectV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	v, err := s.projectDTO(r.Context(), project, p)
	if err != nil {
		apiError(w, 404, "project_not_found", "project unavailable")
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) servicesV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	v, err := s.projectDTO(r.Context(), project, p)
	if err != nil {
		apiError(w, 404, "project_not_found", "project unavailable")
		return
	}
	for i := range v.Services {
		app, err := s.lookupApp(r.Context(), v.Services[i].ID)
		if err == nil {
			b, _ := json.Marshal(s.observeApp(r.Context(), app))
			_ = json.Unmarshal(b, &v.Services[i].Deployment)
		}
	}
	writeJSON(w, 200, map[string]any{"services": v.Services})
}
func (s *Server) lookupApp(ctx context.Context, name string) (store.App, error) {
	app, err := s.cfg.Store.AppByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return s.cfg.Store.ImageAppByName(ctx, name)
	}
	return app, err
}

func (s *Server) reserveProjectV2(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	project := r.PathValue("project")
	if !spec.NamePattern.MatchString(project) {
		apiError(w, 400, "invalid_project", "invalid project name")
		return
	}
	policy := api.ProjectPolicy{Namespace: project, MaxServices: 20, MaxReplicas: 10}
	b, _ := json.Marshal(policy)
	err := s.cfg.Store.CreateObject(r.Context(), store.Object{Kind: "policy", ID: project, Project: project, Owner: "admin", Payload: b})
	if errors.Is(err, store.ErrDuplicate) {
		apiError(w, 409, "project_exists", "project is already reserved")
		return
	}
	if err != nil {
		apiError(w, 500, "storage_error", "cannot reserve project")
		return
	}
	writeJSON(w, 201, map[string]string{"project": project})
}
func (s *Server) policyFor(ctx context.Context, project string, previous promote.ProjectManifest) (api.ProjectPolicy, string, error) {
	o, err := s.cfg.Store.Object(ctx, "policy", project)
	if err == nil {
		var p api.ProjectPolicy
		err = json.Unmarshal(o.Payload, &p)
		return p, spec.HashBytes(o.Payload), err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return api.ProjectPolicy{}, "", err
	}
	if previous.Project == "" {
		return api.ProjectPolicy{}, "", store.ErrNotFound
	}
	// Legacy projects keep their existing operator settings until explicitly
	// configured; newly requested exceptions are never inferred from a manifest.
	p := api.ProjectPolicy{Namespace: promote.ProjectNamespace(previous, project), Infisical: previous.Infisical, MaxServices: 20, MaxReplicas: 10}
	for _, svc := range previous.Services {
		if svc.Repository != "" && !slices.Contains(p.Repositories, svc.Repository) {
			p.Repositories = append(p.Repositories, svc.Repository)
		}
		if svc.Hostname != "" && !slices.Contains(p.Domains, svc.Hostname) {
			p.Domains = append(p.Domains, svc.Hostname)
		}
	}
	sort.Strings(p.Repositories)
	sort.Strings(p.Domains)
	b, _ := json.Marshal(p)
	return p, spec.HashBytes(b), nil
}
func (s *Server) policyV2(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	project := r.PathValue("project")
	if !spec.NamePattern.MatchString(project) {
		apiError(w, 400, "invalid_project", "invalid project")
		return
	}
	var p api.ProjectPolicy
	if err := readJSON(r, &p); err != nil {
		apiError(w, 400, "invalid_policy", err.Error())
		return
	}
	_, old, err := s.projectManifest(r.Context(), project)
	if err != nil && !errors.Is(err, promote.ErrProjectNotFound) {
		apiError(w, 502, "gitops_unavailable", "cannot inspect existing project")
		return
	}
	if p.Namespace == "" {
		p.Namespace = project
	}
	if !spec.NamePattern.MatchString(p.Namespace) {
		apiError(w, 400, "invalid_namespace", "invalid namespace")
		return
	}
	if old.Project != "" && (p.Namespace != promote.ProjectNamespace(old, project) || !reflect.DeepEqual(p.Infisical, old.Infisical)) {
		apiError(w, 409, "migration_required", "namespace and secrets identity changes require an explicit migration")
		return
	}
	if p.MaxServices <= 0 || p.MaxReplicas <= 0 {
		apiError(w, 400, "invalid_quota", "positive max_services and max_replicas are required")
		return
	}
	b, _ := json.Marshal(p)
	o := store.Object{Kind: "policy", ID: project, Project: project, Owner: "admin", Payload: b}
	if _, err := s.cfg.Store.Object(r.Context(), "policy", project); errors.Is(err, store.ErrNotFound) {
		err = s.cfg.Store.CreateObject(r.Context(), o)
		if err != nil {
			apiError(w, 500, "storage_error", "cannot save policy")
			return
		}
	} else if err := s.cfg.Store.SaveObject(r.Context(), o); err != nil {
		apiError(w, 500, "storage_error", "cannot save policy")
		return
	}
	writeJSON(w, 200, p)
}

func checkManifestChange(old, m promote.ProjectManifest, p api.ProjectPolicy) error {
	if m.Namespace != "" && m.Namespace != p.Namespace {
		return fmt.Errorf("namespace is managed by the administrator")
	}
	if m.Infisical != nil && !reflect.DeepEqual(m.Infisical, p.Infisical) {
		return fmt.Errorf("secrets identity is managed by the administrator")
	}
	if len(m.Services) > p.MaxServices {
		return fmt.Errorf("project service quota exceeded")
	}
	for name, was := range old.Addons {
		now, ok := m.Addons[name]
		if !ok || !reflect.DeepEqual(was, now) {
			return fmt.Errorf("addon %s requires an administrator migration", name)
		}
	}
	for name, was := range old.Services {
		now, exists := m.Services[name]
		for _, volume := range was.Volumes {
			if volume.Size != "" || volume.Claim != "" {
				if !exists || !slices.ContainsFunc(now.Volumes, func(v promote.Volume) bool { return reflect.DeepEqual(v, volume) }) {
					return fmt.Errorf("persistent volume %s/%s requires an administrator migration", name, volume.Name)
				}
			}
		}
	}
	for name, v := range m.Services {
		if v.RunAsRoot && !p.AllowRoot {
			return fmt.Errorf("service %s: running as root is not authorized", name)
		}
		if v.WritableRoot && !p.AllowWritableRoot {
			return fmt.Errorf("service %s: writable root is not authorized", name)
		}
		for _, cap := range v.Capabilities {
			if !slices.Contains(p.Capabilities, cap) {
				return fmt.Errorf("service %s: capability %s is not authorized", name, cap)
			}
		}
		if v.Replicas != nil && *v.Replicas > p.MaxReplicas {
			return fmt.Errorf("service %s: replica quota exceeded", name)
		}
		if v.Repository != "" && !slices.Contains(p.Repositories, v.Repository) {
			return fmt.Errorf("service %s: source repository is not authorized; ask the administrator to configure repositories for this project", name)
		}
		if v.Hostname != "" && !slices.ContainsFunc(p.Domains, func(d string) bool { return v.Hostname == d || strings.HasSuffix(v.Hostname, "."+d) }) {
			return fmt.Errorf("service %s: hostname is not authorized; ask the administrator to configure domains for this project", name)
		}
	}
	return nil
}

func (s *Server) planV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	var body api.PlanRequest
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, "invalid_request", err.Error())
		return
	}
	if body.Action == "" {
		body.Action = "deploy"
	}
	switch body.Action {
	case "destroy":
		if !p.isAdmin() {
			apiError(w, 403, "admin_required", "only an administrator may destroy a project")
			return
		}
	case "deploy":
		if !p.canPublish() || len(p.Services) > 0 {
			apiError(w, 403, "maintainer_required", "publishing a manifest requires a maintainer token")
			return
		}
	case "redeploy":
		if !p.canDeploy() {
			apiError(w, 403, "deployer_required", "a deployer token is required")
			return
		}
	default:
		apiError(w, 400, "invalid_action", "unknown plan action")
		return
	}
	raw, old, err := s.projectManifest(r.Context(), project)
	if errors.Is(err, promote.ErrProjectNotFound) {
		raw = nil
		old = promote.ProjectManifest{}
	} else if err != nil {
		apiError(w, 502, "gitops_unavailable", "cannot read the current manifest")
		return
	}
	policy, policyHash, err := s.policyFor(r.Context(), project, old)
	if err != nil {
		apiError(w, 404, "project_not_found", "the administrator must reserve the project first")
		return
	}
	base := ""
	if len(raw) > 0 {
		base = spec.HashBytes(raw)
	}
	if body.ExpectedRevision != nil && *body.ExpectedRevision != base {
		apiError(w, 409, "stale_manifest", "project changed; retrieve the complete manifest before continuing")
		return
	}
	nonce, err := deliveryNonce()
	if err != nil {
		apiError(w, 500, "randomness_unavailable", "cannot generate plan identifier")
		return
	}
	plan := api.Plan{ID: "plan_" + nonce, Project: project, Action: body.Action, BaseRevision: base, PolicyRevision: policyHash, ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Changes: []api.Change{}, Targets: []api.Target{}}
	if body.Action == "destroy" {
		if err := s.checkDestroy(r.Context(), project, old, policy.Namespace); err != nil {
			apiError(w, 409, "destruction_blocked", err.Error())
			return
		}
		plan.Changes = append(plan.Changes, api.Change{Resource: project, Action: "destroy"})
	} else {
		m := old
		if body.Action == "deploy" {
			m, err = promote.ParseProjectManifest([]byte(body.Manifest))
			if err != nil {
				apiError(w, 400, "invalid_manifest", err.Error())
				return
			}
			if m.Project != project {
				apiError(w, 400, "project_mismatch", "manifest project does not match the target")
				return
			}
			if err := checkManifestChange(old, m, policy); err != nil {
				apiError(w, 403, "policy_denied", err.Error())
				return
			}
			m.Namespace = policy.Namespace
			m.Infisical = policy.Infisical
		}
		if body.Action == "redeploy" {
			v, exists := m.Services[body.Service]
			if !exists || v.Repository == "" {
				apiError(w, 400, "source_service_required", "select a registered repository service")
				return
			}
			if len(p.Services) > 0 && !slices.Contains(p.Services, body.Service) {
				apiError(w, 403, "service_forbidden", "token is restricted to other services")
				return
			}
		}
		names := []string{}
		for name := range m.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := m.Services[name]
			if body.Action == "redeploy" && name != body.Service {
				continue
			}
			t := api.Target{Namespace: policy.Namespace, Service: name, App: project + "-" + name, Repository: v.Repository, Branch: v.Branch, Image: v.Image}
			if v.Repository != "" {
				provider, owner, repo, e := git.Resolve(v.Repository, s.cfg.Provider, s.cfg.Token)
				if e != nil {
					apiError(w, 400, "invalid_repository", e.Error())
					return
				}
				if t.Branch == "" {
					metadata, e := provider.Repository(r.Context(), owner, repo)
					if e != nil {
						apiError(w, 502, "source_unavailable", "cannot read source repository")
						return
					}
					t.Branch = metadata.DefaultBranch
					v.Branch = t.Branch
					m.Services[name] = v
				}
				t.Commit, e = provider.BranchSHA(r.Context(), owner, repo, t.Branch)
				if e != nil {
					apiError(w, 502, "source_unavailable", "cannot resolve branch HEAD")
					return
				}
				// Until all forge providers expose ancestry verification, explicit
				// commits must equal this branch's verified HEAD. Fail closed.
				if body.Commit != "" && body.Commit != t.Commit {
					apiError(w, 409, "commit_not_head", "the explicit commit must equal the configured branch HEAD")
					return
				}
			} else {
				digest, e := oci.ResolveDigestWithPolicy(r.Context(), v.Image, s.cfg.Egress)
				if e != nil {
					apiError(w, 400, "image_unavailable", "cannot resolve image digest")
					return
				}
				t.Image = oci.Pin(v.Image, digest)
				v.Image = t.Image
				m.Services[name] = v
			}
			plan.Targets = append(plan.Targets, t)
		}
		// The saved plan contains immutable references. No branch/tag will be
		// resolved again at execution time.
		b, _ := yaml.Marshal(m)
		plan.Manifest = string(b)
		for i := range plan.Targets {
			plan.Targets[i].ConfigurationRevision = spec.Hash(m)
		}
		if body.Action == "deploy" {
			req := s.requestProject(r.Context(), project)
			req.Manifest = m
			req.Raw = plan.Manifest
			req.ExpectedManifestHash = &plan.BaseRevision
			if _, err := promote.PreviewProject(r.Context(), req); err != nil {
				apiError(w, 400, "compile_failed", err.Error())
				return
			}
			for _, name := range names {
				v := m.Services[name]
				oldV, exists := old.Services[name]
				action := "unchanged"
				if !exists {
					action = "add"
				} else if !reflect.DeepEqual(oldV, v) {
					action = "update"
				}
				plan.Changes = append(plan.Changes, api.Change{Resource: "service/" + name, Action: action})
			}
			for name := range old.Services {
				if _, exists := m.Services[name]; !exists {
					plan.Changes = append(plan.Changes, api.Change{Resource: "service/" + name, Action: "remove"})
					plan.Targets = append(plan.Targets, api.Target{Service: name, App: project + "-" + name, Namespace: promote.ProjectNamespace(old, project), Removed: true})
				}
			}
			for name := range m.Addons {
				if _, exists := old.Addons[name]; !exists {
					plan.Changes = append(plan.Changes, api.Change{Resource: "addon/" + name, Action: "add"})
				}
			}
		} else {
			plan.Changes = append(plan.Changes, api.Change{Resource: "service/" + body.Service, Action: "rebuild"})
		}
	}
	b, _ := json.Marshal(plan)
	if err := s.cfg.Store.CreateObject(r.Context(), store.Object{Kind: "plan", ID: plan.ID, Project: project, Owner: p.ownerKey(), Payload: b}); err != nil {
		apiError(w, 500, "storage_error", "cannot save plan")
		return
	}
	if len(p.Services) > 0 {
		plan.Manifest = ""
	}
	writeJSON(w, 201, plan)
}

func (s *Server) checkDestroy(ctx context.Context, project string, m promote.ProjectManifest, namespace string) error {
	if !s.cfg.TenantIsolation && m.Project != "" {
		return fmt.Errorf("migrate to tenant isolation before destruction; the legacy namespace may be owned by Flux")
	}
	if len(m.Addons) > 0 {
		return fmt.Errorf("project declares addons; decommission their data first")
	}
	for _, v := range m.Services {
		for _, vol := range v.Volumes {
			if vol.Size != "" || vol.Claim != "" {
				return fmt.Errorf("project contains persistent service volumes")
			}
		}
	}
	if s.cfg.Kube == nil {
		return fmt.Errorf("the live cluster inventory is required")
	}
	ns, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && m.Project == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot verify namespace ownership")
	}
	if ns.Labels["kuberpack.io/project"] != project {
		return fmt.Errorf("namespace ownership cannot be verified")
	}
	pvc, err := s.cfg.Kube.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot inventory persistent claims")
	}
	if len(pvc.Items) > 0 {
		return fmt.Errorf("namespace contains persistent claims")
	}
	// Namespace deletion has a much larger surface than the manifest. Keep the
	// namespace and refuse foreign workloads instead of claiming it is empty.
	pods, err := s.cfg.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot inventory namespace workloads")
	}
	for _, pod := range pods.Items {
		if pod.Labels["app.kubernetes.io/part-of"] != "kuberpack" {
			return fmt.Errorf("namespace contains foreign workloads")
		}
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil || vol.CSI != nil || vol.HostPath != nil {
				return fmt.Errorf("namespace contains a persistent or external volume")
			}
		}
	}
	return nil
}

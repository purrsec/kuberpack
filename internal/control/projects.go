package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type projectBody struct {
	Project  string `json:"project"`
	Manifest string `json:"manifest"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := parseProjectManifest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Un jeton de projet ne compile que son projet : le manifeste ne doit
	// pas permettre d'en écrire un autre.
	if _, ok := requireProject(w, r, m.Project); !ok {
		return
	}
	if s.cfg.GitOpsURL == "" {
		http.Error(w, "gitops is not configured", http.StatusForbidden)
		return
	}
	res, err := promote.EnsureProject(r.Context(), promote.ProjectRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		AuthArgs:     s.cfg.Provider.CloneAuth(s.cfg.Token),
		Manifest:     m,
		Raw:          body.Manifest,
		Platform:     s.cfg.Platform,
		NamespaceCheck: func(project, namespace string) error {
			return s.checkNamespaceOwnership(r.Context(), project, namespace)
		},
	})
	if err != nil {
		http.Error(w, "compile failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	// The project is applied by Flux with its own tenant identity. The
	// namespace and the identity must exist before the application is
	// reconciled.
	if err := s.ensureProjectTenant(r.Context(), res.Project, res.Namespace); err != nil {
		http.Error(w, "the manifest was applied, but its tenant could not be provisioned: "+err.Error(), http.StatusBadGateway)
		return
	}
	// The manifest is the source of truth: the declared services become apps
	// with a webhook and a build. The GitOps is already written, so a retry
	// of the same manifest arms the pipeline.
	if err := s.reconcileApps(r.Context(), res.Project, res.Apps); err != nil {
		http.Error(w, "the manifest was applied, but the build pipeline could not be armed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.syncProjectTenantScope(r.Context()); err != nil {
		http.Error(w, "the project was applied, but the Infisical scope could not be updated: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, projectJSON(res))
}

// checkNamespaceOwnership refuses to write a project into a namespace that
// already exists for somebody else. A GitOps apply would otherwise adopt it,
// extend the Infisical operator scope to it and expose its Secrets.
//
// The generated Namespace carries kuberpack.io/project. An administrator who
// deliberately wants to adopt an existing namespace annotates it with
// kuberpack.io/adopt=<project>. The historical layout, where Flux already
// owns the namespace through the projects Kustomization, stays accepted.
func (s *Server) checkNamespaceOwnership(ctx context.Context, project, namespace string) error {
	if s.cfg.Kube == nil {
		return nil
	}
	ns, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read namespace %q: %w", namespace, err)
	}
	if ns.Labels["kuberpack.io/project"] == project {
		return nil
	}
	if ns.Annotations["kuberpack.io/adopt"] == project {
		return nil
	}
	if ns.Labels["app.kubernetes.io/part-of"] == "kuberpack" && ns.Labels["kustomize.toolkit.fluxcd.io/name"] == "kuberpack-projects" {
		return nil
	}
	return fmt.Errorf("namespace %q already exists and is not owned by Kuberpack project %q; delete it, choose another namespace, or annotate it kuberpack.io/adopt=%q", namespace, project, project)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := requireProject(w, r, name); !ok {
		return
	}
	if s.cfg.GitOpsURL == "" {
		http.Error(w, "gitops is not configured", http.StatusForbidden)
		return
	}
	// A project that holds a database is not removed silently: the caller
	// asks for it explicitly.
	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"
	namespace := name
	if apps, lookupErr := s.cfg.Store.AppsByProject(r.Context(), name); lookupErr == nil && len(apps) > 0 {
		namespace = apps[0].Namespace
	}
	res, err := promote.DeleteProject(r.Context(), promote.ProjectRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		AuthArgs:     s.cfg.Provider.CloneAuth(s.cfg.Token),
		Manifest:     promote.ProjectManifest{Project: name},
		Platform:     s.cfg.Platform,
		Force:        force,
	})
	if err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	// The apps of a deleted project lose their webhook and their secret.
	if err := s.reconcileApps(r.Context(), name, nil); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.deleteProjectTenant(r.Context(), name, namespace); err != nil {
		http.Error(w, "the project was removed, but its tenant could not be cleaned: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.syncProjectTenantScope(r.Context()); err != nil {
		http.Error(w, "the project was removed, but the Infisical scope could not be updated: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, projectJSON(res))
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	if s.cfg.GitOpsURL == "" {
		http.Error(w, "gitops is not configured", http.StatusForbidden)
		return
	}
	items, err := promote.ListProjects(r.Context(), promote.ProjectRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		AuthArgs:     s.cfg.Provider.CloneAuth(s.cfg.Token),
		Platform:     s.cfg.Platform,
	})
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		// Un jeton de projet ne voit que son projet. L'administrateur voit
		// tout.
		if !principal.allowsProject(item.Project) {
			continue
		}
		out = append(out, map[string]any{
			"project":   item.Project,
			"namespace": item.Namespace,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := requireProject(w, r, name); !ok {
		return
	}
	if s.cfg.GitOpsURL == "" {
		http.Error(w, "gitops is not configured", http.StatusForbidden)
		return
	}
	raw, err := promote.ReadProjectManifest(r.Context(), promote.ProjectRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		AuthArgs:     s.cfg.Provider.CloneAuth(s.cfg.Token),
		Manifest:     promote.ProjectManifest{Project: name},
		Platform:     s.cfg.Platform,
	})
	if errors.Is(err, promote.ErrProjectNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/yaml")
	_, _ = w.Write(raw)
}

func parseProjectManifest(body projectBody) (promote.ProjectManifest, error) {
	var m promote.ProjectManifest
	text := strings.TrimSpace(body.Manifest)
	if text == "" {
		if strings.TrimSpace(body.Project) == "" {
			return m, errors.New("project is required")
		}
		m.Project = strings.TrimSpace(body.Project)
		return m, nil
	}
	// The manifest is parsed strictly: an unknown field is an error, so the
	// specification and the compiler cannot drift apart silently.
	m, err := promote.ParseProjectManifest([]byte(text))
	if err != nil {
		return m, err
	}
	if name := strings.TrimSpace(body.Project); name != "" && name != m.Project {
		return m, errors.New("project must match the name in the manifest")
	}
	if strings.TrimSpace(m.Project) == "" {
		return m, errors.New("manifest must set project")
	}
	return m, nil
}

func projectJSON(res promote.ProjectResult) map[string]any {
	return map[string]any{
		"project":      res.Project,
		"services":     res.Services,
		"addons":       res.Addons,
		"infra_commit": res.InfraCommitSHA,
		"changed":      res.Changed,
	}
}

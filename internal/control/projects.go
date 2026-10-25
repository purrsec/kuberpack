package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type projectBody struct {
	Project  string `json:"project"`
	Manifest string `json:"manifest"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	apiError(w, 410, "upgrade_required", "use the v2 plan/deployment API")
	return
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
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	apiError(w, 410, "upgrade_required", "use an administrator destruction plan")
	return
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
	p, _ := principalFrom(r.Context())
	if len(p.Services) > 0 {
		apiError(w, 403, "manifest_forbidden", "service-restricted tokens cannot read the complete manifest")
		return
	}
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

func projectJSON(res promote.ProjectResult) map[string]any {
	return map[string]any{
		"project":      res.Project,
		"services":     res.Services,
		"addons":       res.Addons,
		"infra_commit": res.InfraCommitSHA,
		"changed":      res.Changed,
	}
}

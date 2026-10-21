package control

import (
	"errors"
	"net/http"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/promote"
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
	})
	if err != nil {
		http.Error(w, "compile failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	// The manifest is the source of truth: the declared services become apps
	// with a webhook and a build. The GitOps is already written, so a retry
	// of the same manifest arms the pipeline.
	if err := s.reconcileApps(r.Context(), res.Project, res.Apps); err != nil {
		http.Error(w, "the manifest was applied, but the build pipeline could not be armed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, projectJSON(res))
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.cfg.GitOpsURL == "" {
		http.Error(w, "gitops is not configured", http.StatusForbidden)
		return
	}
	// A project that holds a database is not removed silently: the caller
	// asks for it explicitly.
	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"
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
	writeJSON(w, http.StatusOK, projectJSON(res))
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
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
		out = append(out, map[string]any{
			"project":   item.Project,
			"namespace": item.Namespace,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
	if strings.TrimSpace(body.Project) != "" {
		m.Project = strings.TrimSpace(body.Project)
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

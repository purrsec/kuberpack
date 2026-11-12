package control

import (
	"net/http"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/promote"
)

// compileV2 runs the real compiler on a manifest and returns its verdict
// without committing anything. It exists because a push that is refused
// leaves the GitOps repository untouched: no commit is made, so the
// explanation reaches the developer only through the commit status or the
// log of the platform, after the round trip.
//
// It is the same compilation a push performs — manifest validation,
// capacity, namespace ownership, addon operators, secrets scope, chart
// reference — through PreviewProject, the preflight the plan already uses.
// Only the commit and the push are absent.
func (s *Server) compileV2(w http.ResponseWriter, r *http.Request) {
	p, project, ok := s.projectAccess(w, r)
	if !ok {
		return
	}
	if !p.canPublish() {
		apiError(w, http.StatusForbidden, "maintainer_required", "compiling a manifest requires a maintainer token")
		return
	}
	var body api.CompileRequest
	if err := readJSON(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	m, err := promote.ParseProjectManifest([]byte(body.Manifest))
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_manifest", err.Error())
		return
	}
	if m.Project != project {
		apiError(w, http.StatusBadRequest, "project_mismatch", "manifest project does not match the target")
		return
	}
	req := s.requestProject(r.Context(), project)
	req.Manifest = m
	req.Raw = body.Manifest
	result, err := promote.PreviewProject(r.Context(), req)
	if err != nil {
		// A refusal is a result: the developer wants the reason, not a
		// status code that says nothing about what to change.
		writeJSON(w, http.StatusOK, api.CompileResult{Valid: false, Project: project, Refused: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, api.CompileResult{
		Valid:     true,
		Project:   project,
		Namespace: result.Namespace,
		Services:  result.Services,
		Addons:    result.Addons,
	})
}

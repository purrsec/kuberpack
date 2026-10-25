package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"git.host.bzh/pepe/kuberpack/internal/spec"
)

// PreviewProject executes the same compiler and capacity checks in an isolated
// checkout, without committing, pushing, creating a webhook or accessing Kube.
func PreviewProject(ctx context.Context, req ProjectRequest) (ProjectResult, error) {
	if err := validateManifest(req.Manifest); err != nil {
		return ProjectResult{}, err
	}
	if err := checkCapacity(req.Manifest, req.Platform); err != nil {
		return ProjectResult{}, err
	}
	parent, err := os.MkdirTemp("", "kuberpack-plan-")
	if err != nil {
		return ProjectResult{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	branch := req.GitOpsBranch
	if branch == "" {
		branch = "main"
	}
	if _, err := req.remoteGit(ctx, "", "clone", "--branch", branch, "--single-branch", req.GitOpsURL, work); err != nil {
		return ProjectResult{}, err
	}
	if err := guardProjectTree(work, req.Platform); err != nil {
		return ProjectResult{}, err
	}
	if err := checkClusterAddition(work, req.Platform, req.Manifest); err != nil {
		return ProjectResult{}, err
	}
	if req.NamespaceCheck != nil {
		if err := req.NamespaceCheck(req.Manifest.Project, ProjectNamespace(req.Manifest, req.Manifest.Project)); err != nil {
			return ProjectResult{}, err
		}
	}
	if err := checkExpectedManifest(work, req); err != nil {
		return ProjectResult{}, err
	}
	services, addons, apps, err := compileProjectFiles(ctx, work, req.Manifest, req.Platform, req.Raw, true)
	return ProjectResult{Project: req.Manifest.Project, Namespace: ProjectNamespace(req.Manifest, req.Manifest.Project), Services: services, Addons: addons, Apps: apps}, err
}

func checkExpectedManifest(work string, req ProjectRequest) error {
	if req.ExpectedManifestHash == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(req.Platform.ProjectManifestPath(req.Manifest.Project))))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	got := ""
	if err == nil {
		got = spec.HashBytes(raw)
	}
	if got != *req.ExpectedManifestHash {
		return fmt.Errorf("project manifest changed since the plan was prepared")
	}
	return nil
}

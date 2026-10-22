package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

// ReloadRequest recompiles the projects of the GitOps repository.
type ReloadRequest struct {
	GitOpsURL    string
	GitOpsBranch string
	HTTPToken    string
	// AuthArgs are the git arguments that authenticate the clone of the
	// GitOps repository. Every forge has its own way.
	AuthArgs []string
	// AuthScheme is how the forge of the GitOps repository authenticates a
	// clone: "token" for a Gitea, "basic" for GitHub and GitLab.
	AuthScheme  string
	MaxAttempts int
	// Projects are the names of the projects to reload. Empty means every
	// project of the repository.
	Projects []string
	Platform Platform
}

// ReloadResult is the outcome of a reload.
type ReloadResult struct {
	// Projects are the results of the recompiled projects.
	Projects []ProjectResult
	// Removed are the projects whose manifest disappeared: they are deleted
	// from the GitOps.
	Removed []string
	// Refused are the projects that were not compiled because they do not
	// fit in the node. The others are deployed: a platform that does not
	// fit refuses the project that tips it over, not everything.
	Refused []string
	// InfraCommitSHA is the commit that carries the recompilation.
	InfraCommitSHA string
	Changed        bool
}

// ReloadProjects recompiles the projects whose manifest lives in the GitOps
// repository.
//
// The manifest is read from the repository and never rewritten: it is the
// source of truth. A project whose manifest disappeared is deleted, so
// removing a manifest from Git removes the deployment.
//
// Everything happens in one clone and one commit: a push that touches three
// manifests is one commit, not three.
func ReloadProjects(ctx context.Context, req ReloadRequest) (ReloadResult, error) {
	var result ReloadResult
	if req.GitOpsURL == "" {
		return result, fmt.Errorf("gitops URL is empty")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	platform := req.Platform.withDefaults()

	parent, err := os.MkdirTemp("", "kuberpack-reload-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")

	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return result, err
		}
		if _, err := cloneGitOps(ctx, req, work); err != nil {
			return result, err
		}

		wanted, err := reloadTargets(work, req, platform)
		if err != nil {
			return result, err
		}

		result = ReloadResult{}
		for _, name := range wanted {
			manifestPath := filepath.Join(work, filepath.FromSlash(platform.ProjectManifestPath(name)))
			raw, err := os.ReadFile(manifestPath)
			if os.IsNotExist(err) {
				// The manifest disappeared: the project goes with it.
				if err := removeProjectFiles(work, name, platform); err != nil {
					return result, err
				}
				result.Removed = append(result.Removed, name)
				continue
			}
			if err != nil {
				return result, err
			}
			manifest, err := ParseProjectManifest(raw)
			if err != nil {
				return result, fmt.Errorf("project %s: %w", name, err)
			}
			// The platform must fit in the node. It is the total that
			// kills a cluster, not one project: the project that tips
			// the total over is refused, and the others are deployed.
			if err := checkClusterAddition(work, platform, manifest); err != nil {
				result.Refused = append(result.Refused, name+": "+err.Error())
				continue
			}
			services, adds, apps, err := compileProjectFiles(ctx, work, manifest, platform, string(raw), false)
			if err != nil {
				return result, fmt.Errorf("project %s: %w", name, err)
			}
			result.Projects = append(result.Projects, ProjectResult{
				Project:        sanitizeName(manifest.Project),
				Namespace:      projectNamespace(manifest, sanitizeName(manifest.Project)),
				Services:       services,
				Addons:         adds,
				Apps:           apps,
				InfraCommitSHA: "",
			})
		}

		// The projects parent and the project directories are the only paths
		// a recompilation touches.
		projectsRel := platform.ProjectsDirPath()
		status, err := runGit(ctx, work, "status", "--porcelain", "--", projectsRel)
		if err != nil {
			return result, err
		}
		if status == "" {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return result, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}
		if _, err := runGit(ctx, work, gitConfig("add", "-A", "--", projectsRel)...); err != nil {
			return result, err
		}
		msg := fmt.Sprintf("compile %d project(s)", len(result.Projects))
		if len(result.Refused) > 0 {
			msg = fmt.Sprintf("%s, %d refused", msg, len(result.Refused))
		}
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", msg)...); err != nil {
			return result, err
		}
		if _, err := pushGitOps(ctx, req, work); err != nil {
			lastErr = err
			if _, fetchErr := runGit(ctx, work, "fetch", "origin", req.GitOpsBranch); fetchErr != nil {
				return result, err
			}
			local, _ := runGit(ctx, work, "rev-parse", "HEAD")
			remote, _ := runGit(ctx, work, "rev-parse", "origin/"+req.GitOpsBranch)
			if local != "" && local == remote {
				return result, err
			}
			continue
		}
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return result, err
		}
		for i := range result.Projects {
			result.Projects[i].InfraCommitSHA = sha
		}
		result.InfraCommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops conflict on %s after %d attempts", req.GitOpsBranch, req.MaxAttempts)
	}
	return result, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

// reloadTargets lists the projects to recompile: the ones the caller asked
// for, or every project of the repository.
func reloadTargets(work string, req ReloadRequest, platform Platform) ([]string, error) {
	if len(req.Projects) > 0 {
		out := make([]string, 0, len(req.Projects))
		seen := map[string]bool{}
		for _, name := range req.Projects {
			name = sanitizeName(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
		sort.Strings(out)
		return out, nil
	}
	entries, err := os.ReadDir(filepath.Join(work, filepath.FromSlash(platform.ProjectsDirPath())))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := sanitizeName(entry.Name())
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// removeProjectFiles deletes a project from the work tree: its directory and
// its entry in the projects parent, so Flux prunes it.
func removeProjectFiles(work string, project string, platform Platform) error {
	rel := platform.ProjectDir(project)
	dir := filepath.Join(work, filepath.FromSlash(rel))
	if _, err := os.Stat(dir); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	parentPath := filepath.Join(work, filepath.FromSlash(platform.ProjectsParentPath()))
	raw, err := os.ReadFile(parentPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !listedInKustomization(raw, project) {
		return nil
	}
	return os.WriteFile(parentPath, removeKustomizationResource(raw, project), 0o644)
}

// authArgs is how the clone of the GitOps repository is authenticated.
func (req ReloadRequest) authArgs() []string {
	if req.AuthArgs != nil {
		return req.AuthArgs
	}
	return fetch.AuthArgs(req.AuthScheme, req.HTTPToken)
}

// cloneGitOps clones the GitOps repository.
func cloneGitOps(ctx context.Context, req ReloadRequest, work string) (string, error) {
	return runGit(ctx, "", append(req.authArgs(),
		"clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work)...)
}

// pushGitOps pushes the work tree to the GitOps repository.
func pushGitOps(ctx context.Context, req ReloadRequest, work string) (string, error) {
	return runGit(ctx, work, append(req.authArgs(),
		"push", "origin", "HEAD:"+req.GitOpsBranch)...)
}

// ProjectNames lists the projects declared in the repository, from the
// manifests it holds.
func ProjectNames(work string, platform Platform) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(work, filepath.FromSlash(platform.ProjectsDirPath())))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out, nil
}

// IsProjectPath reports whether a path of the GitOps repository belongs to a
// project, and returns the name of the project.
func IsProjectPath(path string, platform Platform) (string, bool) {
	prefix := strings.TrimSuffix(platform.ProjectsDirPath(), "/") + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		return "", false
	}
	return sanitizeName(parts[0]), true
}

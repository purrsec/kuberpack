package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

// ForPreview writes Helm files under PreviewsDir and uses the previews GitOps branch.
func (p Platform) ForPreview() Platform {
	p = p.withDefaults()
	p.AppsDir = p.PreviewsDir
	return p
}

type PreviewRequest struct {
	GitOpsURL    string
	GitOpsBranch string
	HTTPToken    string
	MaxAttempts  int
	App          AppSpec
	Platform     Platform
}

func (req PreviewRequest) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(fetch.TokenHeaderArgs(req.HTTPToken), args...)...)
}

// EnsurePreviewBranch clones the previews branch, creating it if Forgejo has none yet.
func EnsurePreviewBranch(ctx context.Context, gitOpsURL, branch, token string) error {
	if branch == "" {
		branch = "previews"
	}
	parent, err := os.MkdirTemp("", "kuberpack-previews-init-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	extra := fetch.TokenHeaderArgs(token)
	if _, err := runGit(ctx, "", append(extra, "clone", "--branch", branch, "--single-branch", gitOpsURL, work)...); err == nil {
		return nil
	}
	if _, err := runGit(ctx, "", append(extra, "clone", "--branch", "main", "--single-branch", gitOpsURL, work)...); err != nil {
		return fmt.Errorf("clone gitops to create %s: %w", branch, err)
	}
	if _, err := runGit(ctx, work, "checkout", "--orphan", branch); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, gitConfig("rm", "-rf", "--ignore-unmatch", ".")...); err != nil {
		return err
	}
	previews := filepath.Join(work, "kubernetes", "vps", "previews")
	if err := os.MkdirAll(previews, 0o755); err != nil {
		return err
	}
	kustom := []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	if err := os.WriteFile(filepath.Join(previews, "kustomization.yaml"), kustom, 0o644); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, gitConfig("add", "--", "kubernetes/vps/previews")...); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, gitConfig("commit", "-m", "init previews")...); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, append(extra, "push", "-u", "origin", "HEAD:"+branch)...); err != nil {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	return nil
}

// RemovePreview deletes kubernetes/vps/previews/<release> and drops it from the parent list.
func RemovePreview(ctx context.Context, req PreviewRequest) (Result, error) {
	spec, err := normalizeSpec(req.App)
	if err != nil {
		return Result{}, err
	}
	if req.GitOpsURL == "" {
		return Result{}, fmt.Errorf("gitops URL is empty")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = req.Platform.withDefaults().PreviewsBranch
	}
	platform := req.Platform.ForPreview()

	parent, err := os.MkdirTemp("", "kuberpack-preview-rm-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	result := Result{App: spec.Name}

	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
			return Result{}, err
		}
		changed := false
		dir := filepath.Join(work, filepath.FromSlash(platform.AppDir(spec.Name)))
		if _, err := os.Stat(dir); err == nil {
			if err := os.RemoveAll(dir); err != nil {
				return Result{}, err
			}
			changed = true
		}
		path := filepath.Join(work, filepath.FromSlash(platform.ParentPath()))
		raw, err := os.ReadFile(path)
		if err == nil && listedInKustomization(raw, spec.Name) {
			if err := os.WriteFile(path, removeKustomizationResource(raw, spec.Name), 0o644); err != nil {
				return Result{}, err
			}
			changed = true
		}
		if !changed {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return Result{}, err
			}
			result.InfraCommitSHA = sha
			return result, nil
		}
		if _, err := runGit(ctx, work, gitConfig("add", "-A", "--", platform.AppDir(spec.Name), platform.ParentPath())...); err != nil {
			return Result{}, err
		}
		msg := fmt.Sprintf("prune preview %s", spec.Name)
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", msg)...); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
			lastErr = err
			continue
		}
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		result.InfraCommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops push rejected after %d attempts", req.MaxAttempts)
	}
	return Result{}, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

// DisableApp removes an app from the production parent kustomization so Flux can prune it.
func DisableApp(ctx context.Context, req EnsureRequest) (Result, error) {
	spec, err := normalizeSpec(req.App)
	if err != nil {
		return Result{}, err
	}
	if req.GitOpsURL == "" {
		return Result{}, fmt.Errorf("gitops URL is empty")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	platform := req.Platform.withDefaults()
	parent, err := os.MkdirTemp("", "kuberpack-disable-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	result := Result{App: spec.Name}

	if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
		return Result{}, err
	}
	path := filepath.Join(work, filepath.FromSlash(platform.ParentPath()))
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	appDir := filepath.Join(work, filepath.FromSlash(platform.AppDir(spec.Name)))
	appDirExists := false
	if info, statErr := os.Stat(appDir); statErr == nil && info.IsDir() {
		appDirExists = true
	}
	listed := listedInKustomization(raw, spec.Name)
	if !listed && !appDirExists {
		sha, err := runGit(ctx, work, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		result.InfraCommitSHA = sha
		return result, nil
	}
	// Remove the app from the parent kustomization so Flux prunes the workload,
	// and drop its directory so no dead manifest is left behind.
	add := []string{platform.ParentPath()}
	if listed {
		if err := os.WriteFile(path, removeKustomizationResource(raw, spec.Name), 0o644); err != nil {
			return Result{}, err
		}
	}
	if appDirExists {
		if err := os.RemoveAll(appDir); err != nil {
			return Result{}, err
		}
		add = append(add, platform.AppDir(spec.Name))
	}
	if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "-A", "--"}, add...)...)...); err != nil {
		return Result{}, err
	}
	if _, err := runGit(ctx, work, gitConfig("commit", "-m", "unregister "+spec.Name)...); err != nil {
		return Result{}, err
	}
	if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
		return Result{}, err
	}
	sha, err := runGit(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	result.InfraCommitSHA = sha
	result.Changed = true
	return result, nil
}

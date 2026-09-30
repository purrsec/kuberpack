package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/image"
)

const defaultAttempts = 5

// Request is a GitOps digest promotion. It never talks to the Kubernetes API.
type Request struct {
	GitOpsURL    string
	GitOpsBranch string
	ValuesPath   string
	ChartPath    string
	HTTPToken    string
	Image        image.Ref
	MaxAttempts  int
	Hostname     string
	Port         int
	Healthcheck  string
	Platform     Platform
}

// Result is the GitOps commit that pins the image, if a commit was needed.
type Result struct {
	App            string
	Image          string
	InfraCommitSHA string
	Changed        bool
}

// Run clones the GitOps repo, writes `image`, renders the chart, then pushes.
func Run(ctx context.Context, req Request) (Result, error) {
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}

	parent, err := os.MkdirTemp("", "kuberpack-promote-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)

	work := filepath.Join(parent, "repo")
	app := AppName(req.ValuesPath, req.Image.Repository)
	imageStr := req.Image.String()
	result := Result{App: app, Image: imageStr}

	var lastErr error
	for attempt := 1; attempt <= req.MaxAttempts; attempt++ {
		if err := os.RemoveAll(work); err != nil {
			return Result{}, err
		}
		if _, err := req.remoteGit(ctx, "", "clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work); err != nil {
			return Result{}, err
		}

		valuesFile := filepath.Join(work, filepath.FromSlash(req.ValuesPath))
		if _, err := os.Stat(valuesFile); os.IsNotExist(err) {
			spec, err := normalizeSpec(AppSpec{
				Name:        app,
				Hostname:    req.Hostname,
				Port:        req.Port,
				Healthcheck: req.Healthcheck,
			})
			if err != nil {
				return Result{}, fmt.Errorf("read values %s: %w", req.ValuesPath, err)
			}
			if _, err := writeMissingAppFiles(work, spec, req.Platform); err != nil {
				return Result{}, err
			}
		}
		current, err := os.ReadFile(valuesFile)
		if err != nil {
			return Result{}, fmt.Errorf("read values %s: %w", req.ValuesPath, err)
		}

		pinned := alreadyPinned(current, imageStr)
		if !pinned {
			patched, err := PatchImage(current, imageStr)
			if err != nil {
				return Result{}, err
			}
			if err := os.WriteFile(valuesFile, patched, 0o644); err != nil {
				return Result{}, err
			}
		}

		parentChanged, err := enableInParent(work, app, req.Platform)
		if err != nil {
			return Result{}, err
		}

		if _, err := RenderChart(req.ChartPath, app, valuesFile); err != nil {
			return Result{}, err
		}

		add := existingGitPaths(work, req.ValuesPath, req.Platform.withDefaults().AppDir(app), req.Platform.ParentPath())
		status, err := runGit(ctx, work, append([]string{"status", "--porcelain", "--"}, add...)...)
		if err != nil {
			return Result{}, err
		}
		if status == "" && pinned && !parentChanged {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return Result{}, err
			}
			result.InfraCommitSHA = sha
			result.Changed = false
			return result, nil
		}

		if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "--"}, add...)...)...); err != nil {
			return Result{}, err
		}
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", commitMessage(app, imageStr))...); err != nil {
			return Result{}, err
		}

		if _, err := req.remoteGit(ctx, work, "push", "origin", "HEAD:"+req.GitOpsBranch); err != nil {
			lastErr = err
			if _, fetchErr := req.remoteGit(ctx, work, "fetch", "origin", req.GitOpsBranch); fetchErr != nil {
				return Result{}, err
			}
			local, _ := runGit(ctx, work, "rev-parse", "HEAD")
			remote, _ := runGit(ctx, work, "rev-parse", "origin/"+req.GitOpsBranch)
			if local != "" && local == remote {
				return Result{}, err
			}
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

func validateRequest(req Request) error {
	if req.GitOpsURL == "" {
		return fmt.Errorf("gitops URL is empty")
	}
	if req.ValuesPath == "" {
		return fmt.Errorf("values path is empty")
	}
	if filepath.IsAbs(req.ValuesPath) {
		return fmt.Errorf("values path must be relative to the GitOps repository")
	}
	clean := filepath.ToSlash(filepath.Clean(req.ValuesPath))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("values path must be relative to the GitOps repository")
	}
	if req.ChartPath == "" {
		return fmt.Errorf("chart path is empty")
	}
	if req.Image.Repository == "" || req.Image.Tag == "" || req.Image.Digest == "" {
		return fmt.Errorf("image is incomplete")
	}
	return nil
}

func existingGitPaths(work string, paths ...string) []string {
	var out []string
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(p))); err == nil {
			out = append(out, p)
		}
	}
	return out
}

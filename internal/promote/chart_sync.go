package promote

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

// ChartSpec is one chart to write in the GitOps repository.
type ChartSpec struct {
	// Path is the chart on the disk of the control plane.
	Path string
	// Ref is where the chart lives in the GitOps repository, the way the
	// HelmRelease references it.
	Ref string
	// Name is the name of the chart in the message of the commit.
	Name string
}

// ChartRequest syncs the charts of the control plane into the GitOps
// repository.
type ChartRequest struct {
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
	// ChartPath is the chart on the disk of the control plane.
	ChartPath string
	// ChartRef is where the chart lives in the GitOps repository, the way
	// the HelmRelease references it.
	ChartRef string
	// Charts are the other charts to write in the same commit.
	Charts   []ChartSpec
	Platform Platform
}

// authArgs is how the clone of the GitOps repository is authenticated.
func (req ChartRequest) authArgs() []string {
	if req.AuthArgs != nil {
		return req.AuthArgs
	}
	return fetch.AuthArgs(req.AuthScheme, req.HTTPToken)
}

// ChartResult is the outcome of a chart sync.
type ChartResult struct {
	// Changed tells whether the chart in the repository was different.
	Changed bool
	// CommitSHA is the commit that carries the chart.
	CommitSHA string
}

// EnsureChart writes the chart of the control plane in the GitOps
// repository.
//
// The chart is a platform artifact: the control plane renders it to validate
// the values it writes, so the chart that is applied must be the one it
// knows. An operator who keeps an old chart in the repository would
// silently deploy a chart that ignores the values.
//
// The sync is idempotent: an unchanged chart produces no commit.
func EnsureChart(ctx context.Context, req ChartRequest) (ChartResult, error) {
	return EnsureCharts(ctx, req)
}

// EnsureCharts writes the charts of the platform in the GitOps repository.
//
// The chart of the applications and the chart of the control plane itself
// are written in the same commit: the deployment of the platform must
// never drift from the image that runs it.
//
// The sync is idempotent: unchanged charts produce no commit.
func EnsureCharts(ctx context.Context, req ChartRequest) (ChartResult, error) {
	var result ChartResult
	if req.GitOpsURL == "" {
		return result, fmt.Errorf("gitops URL is empty")
	}
	charts := req.chartSpecs()
	for _, chart := range charts {
		if chart.Path == "" {
			return result, fmt.Errorf("chart path is empty")
		}
		if chartRepoPath(chart.Ref) == "" {
			return result, fmt.Errorf("chart reference is empty")
		}
		if _, err := os.Stat(chart.Path); err != nil {
			return result, fmt.Errorf("chart %s: %w", chart.Path, err)
		}
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = defaultAttempts
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}

	parent, err := os.MkdirTemp("", "kuberpack-chart-*")
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
		if _, err := runGit(ctx, "", append(req.authArgs(),
			"clone", "--branch", req.GitOpsBranch, "--single-branch", req.GitOpsURL, work)...); err != nil {
			return result, err
		}
		targets := make([]string, 0, len(charts))
		for _, chart := range charts {
			target := chartRepoPath(chart.Ref)
			dest := filepath.Join(work, filepath.FromSlash(target))
			if err := os.RemoveAll(dest); err != nil {
				return result, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return result, err
			}
			if err := copyTree(chart.Path, dest); err != nil {
				return result, fmt.Errorf("copy chart: %w", err)
			}
			targets = append(targets, target)
		}
		status, err := runGit(ctx, work, append([]string{"status", "--porcelain", "--"}, targets...)...)
		if err != nil {
			return result, err
		}
		if status == "" {
			sha, err := runGit(ctx, work, "rev-parse", "HEAD")
			if err != nil {
				return result, err
			}
			result.CommitSHA = sha
			return result, nil
		}
		if _, err := runGit(ctx, work, gitConfig(append([]string{"add", "-A", "--"}, targets...)...)...); err != nil {
			return result, err
		}
		if _, err := runGit(ctx, work, gitConfig("commit", "-m", "chart: update "+strings.Join(chartNames(charts), ", "))...); err != nil {
			return result, err
		}
		if _, err := runGit(ctx, work, append(req.authArgs(),
			"push", "origin", "HEAD:"+req.GitOpsBranch)...); err != nil {
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
		result.CommitSHA = sha
		result.Changed = true
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gitops conflict on %s after %d attempts", req.GitOpsBranch, req.MaxAttempts)
	}
	return result, fmt.Errorf("gitops conflict on %s after %d attempts: %w", req.GitOpsBranch, req.MaxAttempts, lastErr)
}

// chartSpecs lists the charts to write: the chart of the applications and
// the charts the request adds.
func (req ChartRequest) chartSpecs() []ChartSpec {
	charts := make([]ChartSpec, 0, 1+len(req.Charts))
	if strings.TrimSpace(req.ChartPath) != "" {
		charts = append(charts, ChartSpec{
			Path: strings.TrimSpace(req.ChartPath),
			Ref:  req.ChartRef,
			Name: chartName(req.ChartRef),
		})
	}
	charts = append(charts, req.Charts...)
	return charts
}

// chartName is the name of a chart, for the message of the commit.
func chartName(ref string) string {
	name := filepath.Base(strings.TrimSuffix(strings.TrimSpace(ref), "/"))
	if name == "" || name == "." {
		return "chart"
	}
	return name
}

// chartNames lists the names of the charts, for the message of the commit.
func chartNames(charts []ChartSpec) []string {
	names := make([]string, 0, len(charts))
	for _, chart := range charts {
		name := strings.TrimSpace(chart.Name)
		if name == "" {
			name = chartName(chart.Ref)
		}
		names = append(names, name)
	}
	return names
}

// chartRepoPath is the path of the chart in the GitOps repository.
func chartRepoPath(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "./")
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "oci://") || strings.Contains(ref, "://") {
		// A chart that lives in a registry is not ours to write.
		return ""
	}
	return strings.TrimSuffix(ref, "/")
}

// copyTree copies a directory recursively.
func copyTree(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

// copyFile copies a file, preserving its mode.
func copyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(infoMode(src))
}

// infoMode returns the mode of a file.
func infoMode(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0o644
	}
	return info.Mode().Perm()
}

package buildsarchive

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

type Request struct {
	GitURL      string
	Token       string
	App         string
	CommitSHA   string
	Environment string
	Status      string
	SBOM        []byte
	LogExcerpt  string
	Reason      string
}

type Result struct {
	CommitSHA string
	Skipped   bool
}

// Push commits one build's SBOM and log excerpt. No-op when GitURL is empty.
// This repository must never be a Flux source.
func Push(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.GitURL) == "" {
		return Result{Skipped: true}, nil
	}
	app := sanitize(req.App)
	sha := strings.ToLower(strings.TrimSpace(req.CommitSHA))
	if app == "" || sha == "" {
		return Result{}, fmt.Errorf("app and commit SHA are required")
	}
	env := strings.TrimSpace(req.Environment)
	if env == "" {
		env = "production"
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "unknown"
	}

	parent, err := os.MkdirTemp("", "kuberpack-builds-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	work := filepath.Join(parent, "repo")
	extra := fetch.TokenHeaderArgs(req.Token)
	if _, err := git(ctx, "", extra, "clone", "--depth", "1", req.GitURL, work); err != nil {
		if _, err := git(ctx, "", extra, "clone", req.GitURL, work); err != nil {
			return Result{}, err
		}
	}
	dir := filepath.Join(work, app, sha)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	if len(req.SBOM) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "sbom.spdx.json"), req.SBOM, 0o644); err != nil {
			return Result{}, err
		}
	}
	excerpt := strings.TrimSpace(req.LogExcerpt)
	if len(excerpt) > 64*1024 {
		excerpt = excerpt[len(excerpt)-64*1024:]
	}
	meta := fmt.Sprintf("app: %s\ncommit: %s\nenvironment: %s\nstatus: %s\nreason: %s\n", app, sha, env, status, strings.TrimSpace(req.Reason))
	if err := os.WriteFile(filepath.Join(dir, "build.txt"), []byte(meta+"\n"+excerpt+"\n"), 0o644); err != nil {
		return Result{}, err
	}
	rel := filepath.ToSlash(filepath.Join(app, sha))
	if _, err := git(ctx, work, nil, "-c", "user.name=kuberpack", "-c", "user.email=kuberpack@host.bzh", "-c", "commit.gpgsign=false", "add", "--", rel); err != nil {
		return Result{}, err
	}
	statusOut, err := git(ctx, work, nil, "status", "--porcelain", "--", rel)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(statusOut) == "" {
		shaOut, err := git(ctx, work, nil, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		return Result{CommitSHA: shaOut}, nil
	}
	msg := fmt.Sprintf("build %s %s %s", app, sha[:min(12, len(sha))], status)
	if _, err := git(ctx, work, nil, "-c", "user.name=kuberpack", "-c", "user.email=kuberpack@host.bzh", "-c", "commit.gpgsign=false", "commit", "-m", msg); err != nil {
		return Result{}, err
	}
	if _, err := git(ctx, work, extra, "push", "origin", "HEAD"); err != nil {
		return Result{}, err
	}
	shaOut, err := git(ctx, work, nil, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	return Result{CommitSHA: shaOut}, nil
}

func git(ctx context.Context, dir string, extra []string, args ...string) (string, error) {
	all := append(append([]string{}, extra...), args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(all, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package promote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
)

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(sanitizeGitArgs(args), " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func sanitizeGitArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out)-1; i++ {
		if out[i] != "-c" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(out[i+1]), "http.extraheader=authorization:") {
			out[i+1] = "http.extraHeader=Authorization: ***"
		}
	}
	return out
}

func (req Request) remoteGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, dir, append(fetch.TokenHeaderArgs(req.HTTPToken), args...)...)
}

func gitConfig(args ...string) []string {
	base := []string{
		"-c", "user.name=kuberpack",
		"-c", "user.email=kuberpack@host.bzh",
		"-c", "commit.gpgsign=false",
	}
	return append(base, args...)
}

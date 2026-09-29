package fetch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Checkout clones cloneURL and verifies HEAD is exactly commitSHA.
func Checkout(ctx context.Context, cloneURL, commitSHA, dest string, extraGitArgs []string) error {
	if strings.TrimSpace(cloneURL) == "" {
		return fmt.Errorf("clone URL is empty")
	}
	commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
	if commitSHA == "" {
		return fmt.Errorf("commit SHA is empty")
	}
	if dest == "" {
		return fmt.Errorf("checkout directory is empty")
	}

	args := []string{"clone", "--filter=blob:none", cloneURL, dest}
	if _, err := runGit(ctx, "", extraGitArgs, args...); err != nil {
		return err
	}
	if _, err := runGit(ctx, dest, extraGitArgs, "checkout", "--detach", commitSHA); err != nil {
		return fmt.Errorf("checkout %s: %w", commitSHA, err)
	}
	got, err := runGit(ctx, dest, extraGitArgs, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got != commitSHA {
		return fmt.Errorf("cloned SHA %s does not match requested %s", got, commitSHA)
	}
	return nil
}

func TokenHeaderArgs(token string) []string {
	if token == "" {
		return nil
	}
	return []string{"-c", "http.extraHeader=Authorization: token " + token}
}

func runGit(ctx context.Context, dir string, extra []string, args ...string) (string, error) {
	all := append(append([]string{}, extra...), args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

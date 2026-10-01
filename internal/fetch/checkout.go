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
	return checkout(ctx, cloneURL, commitSHA, dest, extraGitArgs, "")
}

func CheckoutWithSSH(ctx context.Context, cloneURL, commitSHA, dest, keyPath string) error {
	return checkout(ctx, cloneURL, commitSHA, dest, nil, keyPath)
}

func checkout(ctx context.Context, cloneURL, commitSHA, dest string, extraGitArgs []string, sshKey string) error {
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
	if _, err := runGit(ctx, "", extraGitArgs, sshKey, args...); err != nil {
		return err
	}
	if _, err := runGit(ctx, dest, extraGitArgs, sshKey, "checkout", "--detach", commitSHA); err != nil {
		return fmt.Errorf("checkout %s: %w", commitSHA, err)
	}
	got, err := runGit(ctx, dest, extraGitArgs, sshKey, "rev-parse", "HEAD")
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

func runGit(ctx context.Context, dir string, extra []string, sshKey string, args ...string) (string, error) {
	all := append(append([]string{}, extra...), args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if sshKey != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -i "+sshKey+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/tmp/kuberpack-known-hosts")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(sanitizeGitArgs(all), " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
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

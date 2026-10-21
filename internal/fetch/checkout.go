package fetch

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Checkout clones cloneURL and verifies HEAD is exactly commitSHA.
// Kuberpack only uses HTTPS clones (control plane and builder); there is no
// SSH deploy-key path.
//
// The token of the platform is not necessarily valid on the forge of the
// application: a third party repository lives on somebody else's forge. When
// the token is refused, the clone is retried anonymously, which is enough
// for a public repository.
func Checkout(ctx context.Context, cloneURL, commitSHA, dest string, extraGitArgs []string) error {
	err := checkout(ctx, cloneURL, commitSHA, dest, extraGitArgs)
	if err == nil || len(extraGitArgs) == 0 {
		return err
	}
	// The first attempt left a partial clone behind: the retry needs a
	// clean directory.
	if removeErr := os.RemoveAll(dest); removeErr != nil {
		return err
	}
	retryErr := checkout(ctx, cloneURL, commitSHA, dest, nil)
	if retryErr == nil {
		return nil
	}
	return fmt.Errorf("%w (anonymous clone: %v)", err, retryErr)
}

func checkout(ctx context.Context, cloneURL, commitSHA, dest string, extraGitArgs []string) error {
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

// BasicAuthArgs authenticates the clone with the basic scheme: the token is
// the password, the user is a convention ("x-access-token" on GitHub,
// "oauth2" on GitLab).
func BasicAuthArgs(user, token string) []string {
	if token == "" {
		return nil
	}
	if user == "" {
		user = "x-access-token"
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	return []string{"-c", "http.extraHeader=Authorization: Basic " + encoded}
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

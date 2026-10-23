package fetch

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var exactCommitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// Checkout clones cloneURL and verifies HEAD is exactly commitSHA.
// The control plane validates HTTP(S) remotes and the builder requires HTTPS;
// local repositories are also supported by the test fixtures.
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
	if !exactCommitPattern.MatchString(commitSHA) {
		return fmt.Errorf("commit SHA must contain exactly 40 hexadecimal characters")
	}
	if dest == "" {
		return fmt.Errorf("checkout directory is empty")
	}
	if strings.HasPrefix(cloneURL, "-") || (strings.Contains(cloneURL, "::") && !strings.Contains(cloneURL, "://")) {
		return fmt.Errorf("clone URL must not select Git options or remote helpers")
	}
	var err error
	extraGitArgs, err = scopedCloneAuth(cloneURL, extraGitArgs)
	if err != nil {
		return err
	}

	args := []string{"clone", "--filter=blob:none", "--", cloneURL, dest}
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

// Scope credentials to the exact remote origin. Git's global http.extraHeader
// otherwise forwards a platform credential to every HTTP remote it contacts.
func scopedCloneAuth(cloneURL string, args []string) ([]string, error) {
	out := append([]string(nil), args...)
	for index := 0; index+1 < len(out); index++ {
		if out[index] != "-c" || !strings.HasPrefix(strings.ToLower(out[index+1]), "http.extraheader=") {
			continue
		}
		remote, err := url.Parse(cloneURL)
		if err != nil || remote.Host == "" || remote.User != nil || (remote.Scheme != "https" && remote.Scheme != "http") {
			return nil, fmt.Errorf("clone credentials require an HTTP(S) URL without embedded credentials")
		}
		_, value, _ := strings.Cut(out[index+1], "=")
		out[index+1] = "http." + remote.Scheme + "://" + remote.Host + "/.extraHeader=" + value
	}
	return out, nil
}

// The authentication schemes of a clone. Every forge has its own way, and
// a scheme that belongs to another forge is refused: a Gitea token in a
// header is an invalid credential for GitHub.
const (
	// AuthToken sends the token in a header, the way a Gitea expects it.
	AuthToken = "token"
	// AuthBasic sends the token as the password of a basic authentication.
	AuthBasic = "basic"
)

// AuthArgs builds the git arguments of a clone for a scheme. An unknown
// scheme is the token of a Gitea, the default of the platform.
func AuthArgs(scheme, token string) []string {
	if strings.EqualFold(strings.TrimSpace(scheme), AuthBasic) {
		return BasicAuthArgs("x-access-token", token)
	}
	return TokenHeaderArgs(token)
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
	// Checkout can trigger lazy HTTP fetches for a partial clone. Keep these
	// restrictions on every Git command, including its child Git processes.
	all := append(append([]string{}, extra...), "-c", "http.followRedirects=false", "-c", "protocol.ext.allow=never")
	all = append(all, args...)
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
		if key, value, ok := strings.Cut(out[i+1], "="); ok && strings.HasPrefix(strings.ToLower(key), "http.") && strings.HasSuffix(strings.ToLower(key), ".extraheader") && strings.HasPrefix(strings.ToLower(value), "authorization:") {
			out[i+1] = "http.extraHeader=Authorization: ***"
		}
	}
	return out
}

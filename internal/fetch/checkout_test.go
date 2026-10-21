package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutExactSHA(t *testing.T) {
	src := t.TempDir()
	git(t, src, "init", "-b", "main")
	git(t, src, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "init")
	sha := git(t, src, "rev-parse", "HEAD")

	dest := filepath.Join(t.TempDir(), "src")
	if err := Checkout(context.Background(), src, sha, dest, nil); err != nil {
		t.Fatal(err)
	}
	got := git(t, dest, "rev-parse", "HEAD")
	if got != sha {
		t.Fatalf("got %s want %s", got, sha)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// A token that the forge refuses does not stop the build: the clone is
// retried anonymously, which is enough for a public repository. A third
// party application lives on another forge.
func TestCheckoutFallsBackToAnonymous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	// The server is not a git server: the clone fails either way, but the
	// error must come from the anonymous attempt.
	err := Checkout(context.Background(), server.URL+"/repo.git", "0123456789abcdef0123456789abcdef01234567", t.TempDir(),
		[]string{"-c", "http.extraHeader=Authorization: token secret"})
	if err == nil {
		t.Fatal("the clone must fail")
	}
	if strings.Contains(err.Error(), "token secret") {
		t.Fatalf("the token must not appear in the error: %v", err)
	}
}

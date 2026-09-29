package fetch

import (
	"context"
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

package buildsarchive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushNoopWhenUnset(t *testing.T) {
	got, err := Push(context.Background(), Request{App: "web", CommitSHA: "abc"})
	if err != nil || !got.Skipped {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPushCommitsSBOM(t *testing.T) {
	bare := t.TempDir()
	gitT(t, "", "init", "--bare", "-b", "main", bare)
	seed := t.TempDir()
	gitT(t, "", "clone", bare, seed)
	gitT(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "init")
	gitT(t, seed, "push", "origin", "HEAD:main")

	sha := "0123456789abcdef0123456789abcdef01234567"
	got, err := Push(context.Background(), Request{
		GitURL:      bare,
		App:         "pepe/hello-world",
		CommitSHA:   sha,
		Environment: "production",
		Status:      "succeeded",
		SBOM:        []byte(`{"spdxVersion":"SPDX-2.3"}`),
		LogExcerpt:  "published image",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skipped || got.CommitSHA == "" {
		t.Fatalf("%+v", got)
	}
	clone := t.TempDir()
	gitT(t, "", "clone", bare, clone)
	body, err := os.ReadFile(filepath.Join(clone, "pepe-hello-world", sha, "sbom.spdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SPDX-2.3") {
		t.Fatalf("%s", body)
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

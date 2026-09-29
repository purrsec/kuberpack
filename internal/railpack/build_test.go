package railpack

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildReadsDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	writeStub(t, dir, "buildctl", "#!/bin/sh\nexit 0\n")
	writeStub(t, dir, "skopeo", "#!/bin/sh\necho "+digest+"\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	planDir := filepath.Join(t.TempDir(), "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Build(context.Background(), BuildRequest{
		ContextDir: t.TempDir(),
		PlanDir:    planDir,
		Repository: "git.host.bzh/pepe/hello-world",
		Tag:        "sha-0123456789abcdef0123456789abcdef01234567",
		Registry:   "git.host.bzh",
		User:       "pepe",
		Token:      "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != digest {
		t.Fatalf("digest %q", got.Digest)
	}
	if !strings.HasSuffix(got.Image, ":sha-0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("image %q", got.Image)
	}
}

func TestBuildRequiresToken(t *testing.T) {
	if _, err := Build(context.Background(), BuildRequest{
		ContextDir: "x",
		PlanDir:    "y",
		Repository: "git.host.bzh/pepe/app",
		Tag:        "sha-abcdefa",
	}); err == nil {
		t.Fatal("expected error")
	}
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

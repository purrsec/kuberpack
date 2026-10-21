package strategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAutoUV(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	write(t, dir, "uv.lock", "version = 1\n")
	plan, err := Resolve(dir, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != UV {
		t.Fatalf("got %s", plan.Kind)
	}
}

func TestResolveExplicitUVMissingLock(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	if _, err := Resolve(dir, UV); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveAutoRailpack(t *testing.T) {
	dir := t.TempDir()
	plan, err := Resolve(dir, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != Railpack {
		t.Fatalf("got %s", plan.Kind)
	}
}

// A Python project without a lock file cannot be built reproducibly:
// Railpack installs nothing and the image crashes. The build must fail with
// a message that says what to add.
func TestResolveAutoRefusesAPythonProjectWithoutALockFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	_, err := Resolve(dir, Auto)
	if err == nil {
		t.Fatal("the build must fail rather than produce an image without its dependencies")
	}
	if !strings.Contains(err.Error(), "uv.lock") {
		t.Fatalf("the error must say what to add: %v", err)
	}
}

// A requirements.txt is another way to install the dependencies.
func TestResolveAutoAcceptsRequirements(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	write(t, dir, "requirements.txt", "fastapi\n")
	plan, err := Resolve(dir, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != Railpack {
		t.Fatalf("got %s", plan.Kind)
	}
}

// A lock file alone is not a uv project either.
func TestResolveAutoRefusesALockFileAlone(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "uv.lock", "version = 1\n")
	if _, err := Resolve(dir, Auto); err == nil {
		t.Fatal("a lock file alone must fail the build")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

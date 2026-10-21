package strategy

import (
	"os"
	"path/filepath"
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

// The explicit uv strategy uses uv, and resolves the dependencies when the
// repository has no lock: a third party application cannot be asked for one.
func TestResolveExplicitUVWithoutLockGeneratesIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	plan, err := Resolve(dir, UV)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != UV || !plan.GenerateLock {
		t.Fatalf("plan: %+v", plan)
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

// A Python project without a lock file is built: the builder resolves the
// dependencies. The image is pinned by its digest, the deployment stays
// reproducible.
func TestResolveAutoResolvesWithoutALockFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	plan, err := Resolve(dir, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != UV || !plan.GenerateLock {
		t.Fatalf("plan: %+v", plan)
	}
}

// A lock file of the repository is used as it is: the build does not resolve
// anything.
func TestResolveAutoUsesTheLockOfTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	write(t, dir, "uv.lock", "version = 1\n")
	plan, err := Resolve(dir, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != UV || plan.GenerateLock {
		t.Fatalf("plan: %+v", plan)
	}
}

// A requirements.txt is another way to install the dependencies: Railpack
// reads it.
func TestResolveAutoAcceptsRequirements(t *testing.T) {
	dir := t.TempDir()
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

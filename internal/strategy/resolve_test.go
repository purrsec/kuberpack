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

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

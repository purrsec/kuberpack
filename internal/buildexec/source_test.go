package buildexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDirectoryCannotEscapeCheckout(t *testing.T) {
	source, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"../", "../../secrets", outside, "escape", "frontend\\..\\.."} {
		if _, err := buildSourceDirectory(source, root); err == nil {
			t.Fatalf("escaping directory %q accepted", root)
		}
	}
	if got, err := buildSourceDirectory(source, "frontend"); err != nil || got != filepath.Join(source, "frontend") {
		t.Fatalf("valid monorepo directory: %q %v", got, err)
	}
}

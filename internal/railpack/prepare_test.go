package railpack

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrepareSuccess(t *testing.T) {
	withFakeRailpack(t, `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --plan-out) echo '{}' > "$2"; shift 2 ;;
    --info-out) echo '{"success":true}' > "$2"; shift 2 ;;
    *) shift ;;
  esac
done
exit 0
`)
	dir := t.TempDir()
	got, err := Prepare(context.Background(), Request{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Success || got.Frontend != Frontend {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(got.PlanPath); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsApp(t *testing.T) {
	withFakeRailpack(t, `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --info-out) echo '{"success":false}' > "$2"; shift 2 ;;
    --plan-out) echo '{}' > "$2"; shift 2 ;;
    *) shift ;;
  esac
done
exit 1
`)
	if _, err := Prepare(context.Background(), Request{Dir: t.TempDir()}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPrepareRetriesTransient(t *testing.T) {
	state := filepath.Join(t.TempDir(), "n")
	if err := os.WriteFile(state, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	withFakeRailpack(t, `#!/bin/sh
n=$(cat '`+state+`')
echo $((n+1)) > '`+state+`'
while [ "$#" -gt 0 ]; do
  case "$1" in
    --info-out) echo '{"success":true}' > "$2"; shift 2 ;;
    --plan-out) echo '{}' > "$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$n" = "0" ]; then exit 75; fi
exit 0
`)
	if _, err := Prepare(context.Background(), Request{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func withFakeRailpack(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "railpack")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

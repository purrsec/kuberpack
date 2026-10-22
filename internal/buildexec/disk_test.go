package buildexec

import (
	"os"
	"strings"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KiB",
		1536:    "1.5 KiB",
		1 << 20: "1.0 MiB",
		2 << 30: "2.0 GiB",
	}
	for value, want := range cases {
		if got := humanBytes(value); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestMinFreeDiskBytes(t *testing.T) {
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "")
	if got := minFreeDiskBytes(); got != defaultMinFreeDisk {
		t.Errorf("the default is %d, got %d", defaultMinFreeDisk, got)
	}
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "not a number")
	if got := minFreeDiskBytes(); got != defaultMinFreeDisk {
		t.Errorf("a broken value must fall back to the default, got %d", got)
	}
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "5368709120")
	if got := minFreeDiskBytes(); got != 5<<30 {
		t.Errorf("the value of the operator must be used, got %d", got)
	}
}

func TestRequireDiskRefusesABuildThatDoesNotFit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "1099511627776")
	err := requireDisk(dir)
	if err == nil {
		t.Fatal("a build that does not fit must be refused")
	}
	if !strings.Contains(err.Error(), "free") || !strings.Contains(err.Error(), "needs") {
		t.Errorf("the message must say what is missing: %s", err)
	}
}

func TestRequireDiskAcceptsABuildThatFits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "1")
	if err := requireDisk(dir); err != nil {
		t.Fatalf("a build that fits must be accepted: %s", err)
	}
}

func TestRequireDiskIgnoresAMeasurementThatFails(t *testing.T) {
	t.Setenv("KUBERPACK_MIN_FREE_DISK", "1")
	if err := requireDisk("/does/not/exist"); err != nil {
		t.Fatalf("a builder that cannot measure the disk must not refuse the build: %s", err)
	}
}

func TestFreeSpace(t *testing.T) {
	free, err := freeSpace(os.TempDir())
	if err != nil {
		t.Fatalf("measure the disk: %s", err)
	}
	if free == 0 {
		t.Error("the disk of the machine cannot be full")
	}
}

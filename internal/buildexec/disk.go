package buildexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// minFreeDisk is the space a build needs on the node before it starts.
//
// A build writes the layers of the dependencies, the image of the
// application, a copy of it for the scan and the database of the
// vulnerabilities. A node that cannot hold them fails in the middle of the
// build, with a message that says nothing. The builder measures the disk
// first, and says what is missing.
const defaultMinFreeDisk = 2 << 30

// freeSpace returns the bytes that can still be written on the file system
// that holds the path.
func freeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("measure the disk of %s: %w", path, err)
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

// minFreeDiskBytes is the space a build needs. The operator raises it on a
// node that builds big applications.
func minFreeDiskBytes() uint64 {
	raw := strings.TrimSpace(os.Getenv("KUBERPACK_MIN_FREE_DISK"))
	if raw == "" {
		return defaultMinFreeDisk
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return defaultMinFreeDisk
	}
	return value
}

// requireDisk refuses a build that does not fit on the node.
//
// The check is a courtesy: a builder that cannot measure the disk is not
// refused, it fails later with a message of its own.
func requireDisk(path string) error {
	free, err := freeSpace(path)
	if err != nil {
		return nil
	}
	need := minFreeDiskBytes()
	if free >= need {
		return nil
	}
	return fmt.Errorf("the node has %s free, a build needs %s: free the disk of the node, or run the builds on another node",
		humanBytes(free), humanBytes(need))
}

// humanBytes writes a size the way a human reads it.
func humanBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

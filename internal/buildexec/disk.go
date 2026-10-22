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

// evictionThreshold is the share of the disk the kubelet keeps for itself.
//
// Below it, the kubelet starts to evict the pods that write the most: the
// build is the one that writes the most, and it is the first to be
// evicted. A build must not start on a node that is already below this
// threshold, whatever the space it asked for.
const evictionThreshold = 15

// freeSpace returns the bytes that can still be written on the file system
// that holds the path.
func freeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("measure the disk of %s: %w", path, err)
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

// diskTotal returns the size of the file system that holds the path.
func diskTotal(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("measure the disk of %s: %w", path, err)
	}
	return uint64(stat.Blocks) * uint64(stat.Bsize), nil
}

// evictionSpace is the space the kubelet keeps for itself, on the disk of
// the node. It is the space a build must leave, whatever it asks for.
func evictionSpace(path string) uint64 {
	total, err := diskTotal(path)
	if err != nil || total == 0 {
		return 0
	}
	return total * evictionThreshold / 100
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
//
// The space a build needs is the space it asks for, plus the space the
// kubelet keeps for itself: below that threshold, the kubelet evicts the
// pods that write the most, and the build is the first to go.
func requireDisk(path string) error {
	free, err := freeSpace(path)
	if err != nil {
		return nil
	}
	need := minFreeDiskBytes() + buildNeedBytes()
	reserved := evictionSpace(path)
	if reserved > 0 && free < reserved {
		return fmt.Errorf("the node has %s free, and the kubelet keeps %s of the disk for itself: free the disk of the node, or run the builds on another node",
			humanBytes(free), humanBytes(reserved))
	}
	if free >= need {
		return nil
	}
	return fmt.Errorf("the node has %s free, a build needs %s: free the disk of the node, or run the builds on another node",
		humanBytes(free), humanBytes(need))
}

// buildNeedBytes is the space the build writes: its volumes. The platform
// knows them, and tells the builder.
func buildNeedBytes() uint64 {
	raw := strings.TrimSpace(os.Getenv("KUBERPACK_BUILD_DISK_NEED"))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
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

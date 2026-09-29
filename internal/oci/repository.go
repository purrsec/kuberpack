package oci

import (
	"fmt"
	"strings"
)

// SplitRepository splits git.host.bzh/pepe/app into registry host and OCI name.
func SplitRepository(imageRepository string) (host, name string, err error) {
	imageRepository = strings.Trim(strings.TrimSpace(imageRepository), "/")
	if imageRepository == "" {
		return "", "", fmt.Errorf("image repository is empty")
	}
	slash := strings.Index(imageRepository, "/")
	if slash < 1 {
		return "", "", fmt.Errorf("image repository %q must be host/name", imageRepository)
	}
	host = imageRepository[:slash]
	name = imageRepository[slash+1:]
	if host == "" || name == "" || strings.Contains(name, "://") {
		return "", "", fmt.Errorf("image repository %q must be host/name", imageRepository)
	}
	return host, name, nil
}

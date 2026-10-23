package buildexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func buildSourceDirectory(source, root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || root == "." {
		return source, nil
	}
	if !filepath.IsLocal(root) || strings.Contains(root, "\\") {
		return "", fmt.Errorf("root_directory must stay within the repository")
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(source, filepath.FromSlash(root)))
	if err != nil {
		return "", fmt.Errorf("root_directory not found")
	}
	relative, err := filepath.Rel(source, candidate)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("root_directory must not follow a symbolic link outside the repository")
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("root_directory must be a directory in the repository")
	}
	return candidate, nil
}

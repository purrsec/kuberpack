package promote

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// rejectRepoSymlinks prevents versioned symlinks from redirecting reads and
// generated writes outside the temporary checkout. Call it after cloning and
// before touching project-owned paths. Missing paths are safe to create later.
func rejectRepoSymlinks(work string, relativePaths ...string) error {
	for _, relative := range relativePaths {
		relative = filepath.FromSlash(relative)
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("gitOps path must stay within its checkout")
		}
		target := filepath.Join(work, relative)
		current := target
		for current != filepath.Clean(work) {
			info, err := os.Lstat(current)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("gitops path %q must not contain symbolic links", relative)
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			current = filepath.Dir(current)
		}
		if err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) && path == target {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("gitops path %q must not contain symbolic links", relative)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

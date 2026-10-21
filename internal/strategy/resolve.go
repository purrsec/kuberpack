package strategy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Kind string

const (
	Auto     Kind = "auto"
	UV       Kind = "uv"
	Railpack Kind = "railpack"
)

type Plan struct {
	Kind Kind
	// GenerateLock tells the builder to resolve the Python dependencies
	// when the repository has no lock file. A third party application
	// cannot be asked for one: the build resolves it, the deployment stays
	// pinned by the digest of the image.
	GenerateLock bool
}

func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case "", Auto:
		return Auto, nil
	case UV, Railpack:
		return Kind(s), nil
	default:
		return "", fmt.Errorf("unknown strategy %q", s)
	}
}

// Resolve inspects the commit checkout. Explicit uv/railpack do not fall back.
func Resolve(root string, requested Kind) (Plan, error) {
	if requested == "" {
		requested = Auto
	}
	hasUV, err := uvSources(root)
	if err != nil {
		return Plan{}, err
	}

	switch requested {
	case UV:
		if !hasUV {
			return Plan{}, fmt.Errorf("strategy uv requires a pyproject.toml")
		}
		return Plan{Kind: UV, GenerateLock: needsLock(root)}, nil
	case Railpack:
		return Plan{Kind: Railpack}, nil
	case Auto:
		if hasUV {
			// The repository has a lock: it is used as it is.
			return Plan{Kind: UV, GenerateLock: needsLock(root)}, nil
		}
		return Plan{Kind: Railpack}, nil
	default:
		return Plan{}, fmt.Errorf("unknown strategy %q", requested)
	}
}

// uvSources reports whether the checkout is a uv project: a pyproject.toml
// and its lock.
//
// A Python project that has only one of them cannot be built reproducibly:
// Railpack installs nothing and the image starts without its dependencies.
// A failed build with a clear message is better than a deployed image that
// crashes. A requirements.txt is another way to install the dependencies:
// that project is left to Railpack.
func uvSources(root string) (bool, error) {
	py := filepath.Join(root, "pyproject.toml")
	lock := filepath.Join(root, "uv.lock")
	requirements := filepath.Join(root, "requirements.txt")
	_, errPy := os.Stat(py)
	_, errLock := os.Stat(lock)
	_, errReq := os.Stat(requirements)
	if errReq == nil {
		// pip installs the dependencies, whatever the lock says.
		return false, nil
	}
	if errReq != nil && !os.IsNotExist(errReq) {
		return false, errReq
	}
	switch {
	case errPy == nil && errLock == nil:
		return true, nil
	case os.IsNotExist(errPy) && os.IsNotExist(errLock):
		return false, nil
	case os.IsNotExist(errLock):
		// A pyproject.toml without its lock: the build resolves the
		// dependencies. The lock of the repository is used when there is
		// one.
		return true, nil
	case os.IsNotExist(errPy):
		return false, fmt.Errorf("python project: uv.lock without pyproject.toml: add the pyproject.toml or remove the lock")
	default:
		if errPy != nil {
			return false, errPy
		}
		return false, errLock
	}
}

// needsLock reports whether the build has to resolve the Python
// dependencies: the project has a pyproject.toml but no lock.
func needsLock(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "uv.lock")); err == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(root, "pyproject.toml"))
	return err == nil
}

// CheckLock runs `uv lock --check` when uv is on PATH.
func CheckLock(root string) error {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return nil
	}
	cmd := exec.Command(uv, "lock", "--check")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv lock --check: %s", out)
	}
	return nil
}

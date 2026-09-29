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
			return Plan{}, fmt.Errorf("strategy uv requires pyproject.toml and uv.lock")
		}
		return Plan{Kind: UV}, nil
	case Railpack:
		return Plan{Kind: Railpack}, nil
	case Auto:
		if hasUV {
			return Plan{Kind: UV}, nil
		}
		return Plan{Kind: Railpack}, nil
	default:
		return Plan{}, fmt.Errorf("unknown strategy %q", requested)
	}
}

func uvSources(root string) (bool, error) {
	py := filepath.Join(root, "pyproject.toml")
	lock := filepath.Join(root, "uv.lock")
	_, errPy := os.Stat(py)
	_, errLock := os.Stat(lock)
	switch {
	case errPy == nil && errLock == nil:
		return true, nil
	case os.IsNotExist(errPy) && os.IsNotExist(errLock):
		return false, nil
	case os.IsNotExist(errPy) || os.IsNotExist(errLock):
		return false, fmt.Errorf("incomplete uv sources: need both pyproject.toml and uv.lock")
	default:
		if errPy != nil {
			return false, errPy
		}
		return false, errLock
	}
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

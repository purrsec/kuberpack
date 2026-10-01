package railpack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Frontend is the BuildKit frontend used by the k3s builder Job.
const Frontend = "ghcr.io/railwayapp/railpack-frontend:v0.39.0@sha256:db24dc37640b6887c3d455b40876ea30f75182964479670cba6e4cde7ffef103"

const transientExit = 75

type Request struct {
	Dir      string
	WorkDir  string
	StartCmd string
	BuildCmd string
}

type Result struct {
	Success  bool
	PlanPath string
	InfoPath string
	Frontend string
}

// Prepare runs `railpack prepare`. Exit 75 is retried once; exit 1 is final.
func Prepare(ctx context.Context, req Request) (Result, error) {
	if req.Dir == "" {
		return Result{}, fmt.Errorf("railpack directory is empty")
	}
	railpack, err := exec.LookPath("railpack")
	if err != nil {
		return Result{}, fmt.Errorf("railpack CLI not found")
	}

	work := req.WorkDir
	if work == "" {
		work, err = os.MkdirTemp("", "kuberpack-railpack-*")
		if err != nil {
			return Result{}, err
		}
	} else if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, err
	}
	planPath := filepath.Join(work, "railpack-plan.json")
	infoPath := filepath.Join(work, "railpack-info.json")

	run := func() error {
		return runPrepare(ctx, railpack, req, planPath, infoPath)
	}
	if err := run(); err != nil {
		if isTransient(err) {
			if err := run(); err != nil {
				return Result{}, err
			}
		} else {
			return Result{}, err
		}
	}

	success, err := readSuccess(infoPath)
	if err != nil {
		return Result{}, err
	}
	if !success {
		return Result{}, fmt.Errorf("railpack could not prepare this commit")
	}
	return Result{
		Success:  true,
		PlanPath: planPath,
		InfoPath: infoPath,
		Frontend: Frontend,
	}, nil
}

func runPrepare(ctx context.Context, bin string, req Request, planPath, infoPath string) error {
	args := []string{
		"prepare", req.Dir,
		"--plan-out", planPath,
		"--info-out", infoPath,
		"--error-missing-start",
	}
	if req.StartCmd != "" {
		args = append(args, "--start-cmd", req.StartCmd)
	}
	if req.BuildCmd != "" {
		args = append(args, "--build-cmd", req.BuildCmd)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := trimOutput(out)
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &prepareError{code: ee.ExitCode(), msg: msg}
		}
		if msg == "" {
			return err
		}
		return fmt.Errorf("railpack prepare: %s", msg)
	}
	return nil
}

type prepareError struct {
	code int
	msg  string
}

func (e *prepareError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("railpack prepare: exit %d", e.code)
	}
	return fmt.Sprintf("railpack prepare: %s", e.msg)
}

func isTransient(err error) bool {
	var pe *prepareError
	return errors.As(err, &pe) && pe.code == transientExit
}

func readSuccess(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read railpack info: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return false, fmt.Errorf("parse railpack info: %w", err)
	}
	s, ok := raw["success"]
	if !ok {
		return true, nil
	}
	var success bool
	if err := json.Unmarshal(s, &success); err != nil {
		return false, fmt.Errorf("parse railpack info success: %w", err)
	}
	return success, nil
}

func trimOutput(out []byte) string {
	const max = 2048
	s := string(out)
	if len(s) > max {
		return s[:max]
	}
	return s
}

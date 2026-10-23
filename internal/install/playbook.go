// Package install implements the installation contract independently of the CLI.
package install

import (
	"context"
	"fmt"
	"io"
)

// Step observes and reconciles live state. It must be safe to execute again;
// a previous success is never used to skip checking the cluster after a restart.
type Step struct {
	Name string
	Run  func(context.Context) error
}

func Run(ctx context.Context, out io.Writer, steps []Step) error {
	for i, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "[%d/%d] %s\n", i+1, len(steps), step.Name)
		if err := step.Run(ctx); err != nil {
			return fmt.Errorf("installation stopped at %q: %w; fix the cause and rerun the same command (existing credentials and data are preserved)", step.Name, err)
		}
	}
	return nil
}

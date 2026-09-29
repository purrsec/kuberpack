package railpack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func buildkitAddr(ctx context.Context) (string, error) {
	if host := strings.TrimSpace(os.Getenv("BUILDKIT_HOST")); host != "" {
		if err := probeBuildkit(ctx, host); err != nil {
			return "", fmt.Errorf("BUILDKIT_HOST=%s is unreachable", host)
		}
		return host, nil
	}
	for _, host := range []string{
		"unix:///run/buildkit/buildkitd.sock",
		"unix:///var/run/buildkit/buildkitd.sock",
	} {
		if probeBuildkit(ctx, host) == nil {
			return host, nil
		}
	}
	return "", fmt.Errorf("no local BuildKit; trigger dispatches the Forgejo app-builder instead of running buildctl on this machine")
}

func probeBuildkit(ctx context.Context, addr string) error {
	cmd := exec.CommandContext(ctx, "buildctl", "--addr", addr, "debug", "workers")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

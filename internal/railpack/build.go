package railpack

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type BuildRequest struct {
	ContextDir string
	PlanDir    string
	Frontend   string
	Repository string
	Tag        string
	CacheKey   string
	Registry   string
	User       string
	Token      string
}

type BuildResult struct {
	Image  string
	Digest string
}

func Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	if req.ContextDir == "" || req.PlanDir == "" {
		return BuildResult{}, fmt.Errorf("railpack build paths are empty")
	}
	if req.Repository == "" || req.Tag == "" {
		return BuildResult{}, fmt.Errorf("image repository and tag are required")
	}
	if req.Frontend == "" {
		req.Frontend = Frontend
	}
	if req.Token == "" {
		return BuildResult{}, fmt.Errorf("registry token is empty")
	}
	if _, err := exec.LookPath("buildctl"); err != nil {
		return BuildResult{}, fmt.Errorf("buildctl not found")
	}
	if _, err := exec.LookPath("skopeo"); err != nil {
		return BuildResult{}, fmt.Errorf("skopeo not found")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	authDir := filepath.Join(req.PlanDir, "docker")
	authFile, err := writeRegistryAuth(authDir, req.Registry, req.User, req.Token)
	if err != nil {
		return BuildResult{}, err
	}

	addr, err := buildkitAddr(ctx)
	if err != nil {
		return BuildResult{}, err
	}
	fmt.Fprintf(os.Stderr, "buildkit: %s\n", addr)

	image := req.Repository + ":" + req.Tag
	args := []string{
		"--addr", addr,
		"build",
		"--progress", "plain",
		"--local", "context=" + req.ContextDir,
		"--local", "dockerfile=" + req.PlanDir,
		"--frontend", "gateway.v0",
		"--opt", "source=" + req.Frontend,
		"--output", "type=image,name=" + image + ",push=true",
	}
	if req.CacheKey != "" {
		args = append(args, "--opt", "build-arg:cache-key="+req.CacheKey)
	}

	cmd := exec.CommandContext(ctx, "buildctl", args...)
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+authDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return BuildResult{}, fmt.Errorf("buildctl: %w", err)
	}

	digest, err := inspectDigest(ctx, authFile, image)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Image: image, Digest: digest}, nil
}

func writeRegistryAuth(dir, registry, user, token string) (string, error) {
	if registry == "" {
		registry = "git.host.bzh"
	}
	if user == "" {
		user = "pepe"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	body := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, registry, auth)
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		return "", err
	}
	authFile := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authFile, []byte(body), 0o600); err != nil {
		return "", err
	}
	return authFile, nil
}

func inspectDigest(ctx context.Context, authFile, image string) (string, error) {
	cmd := exec.CommandContext(ctx, "skopeo", "inspect", "--authfile", authFile,
		"--format", "{{.Digest}}", "docker://"+image)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("skopeo inspect: %s", msg)
	}
	digest := strings.ToLower(strings.TrimSpace(string(out)))
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("skopeo inspect: unexpected digest %q", digest)
	}
	return digest, nil
}

func RegistryToken() (string, error) {
	if p := strings.TrimSpace(os.Getenv("FORGEJO_REGISTRY_TOKEN_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("registry token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := strings.TrimSpace(os.Getenv("FORGEJO_REGISTRY_TOKEN")); t != "" {
		return t, nil
	}
	if t := strings.TrimSpace(os.Getenv("FORGEJO_TOKEN")); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("set FORGEJO_REGISTRY_TOKEN or FORGEJO_TOKEN to push images")
}

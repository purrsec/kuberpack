package buildexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/railpack"
)

const frontend = "ghcr.io/railwayapp/railpack-frontend:v0.39.0@sha256:db24dc37640b6887c3d455b40876ea30f75182964479670cba6e4cde7ffef103"

var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Request struct {
	CloneURL          string
	CommitSHA         string
	ImageRepository   string
	RegistryUser      string
	StartCmd          string
	GitTokenFile      string
	RegistryTokenFile string
}

// Run builds one commit in a Kubernetes Job. BuildKit runs in a separate
// rootless sidecar; the resulting application image never contains these tools.
func Run(ctx context.Context, req Request) (string, error) {
	if req.CloneURL == "" || req.ImageRepository == "" || req.RegistryUser == "" || !shaPattern.MatchString(req.CommitSHA) {
		return "", fmt.Errorf("clone URL, full lowercase commit SHA, image repository and registry user are required")
	}
	host, name, err := oci.SplitRepository(req.ImageRepository)
	if err != nil {
		return "", err
	}
	if req.GitTokenFile == "" {
		req.GitTokenFile = "/secrets/git-token"
	}
	if req.RegistryTokenFile == "" {
		req.RegistryTokenFile = "/secrets/registry-token"
	}
	gitToken, err := readToken(req.GitTokenFile)
	if err != nil {
		return "", err
	}
	registryToken, err := readToken(req.RegistryTokenFile)
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp("/work", "build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	authDir := filepath.Join(work, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", err
	}
	authFile := filepath.Join(authDir, "config.json")
	auth := base64.StdEncoding.EncodeToString([]byte(req.RegistryUser + ":" + registryToken))
	data, err := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(authFile, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Setenv("DOCKER_CONFIG", authDir); err != nil {
		return "", err
	}
	source := filepath.Join(work, "src")
	if err := fetch.Checkout(ctx, req.CloneURL, req.CommitSHA, source, fetch.TokenHeaderArgs(gitToken)); err != nil {
		return "", err
	}
	fmt.Printf("checked out %s\n", req.CommitSHA)
	if _, err := railpack.Prepare(ctx, railpack.Request{Dir: source, WorkDir: work, StartCmd: req.StartCmd}); err != nil {
		return "", err
	}
	if err := waitForBuildkit(ctx, time.Minute); err != nil {
		return "", err
	}

	tag := "sha-" + req.CommitSHA
	cache := req.ImageRepository + ":buildcache"
	image := req.ImageRepository + ":" + tag
	if err := command(ctx, "buildctl", "--addr", "unix:///run/buildkit/buildkitd.sock", "build",
		"--local", "context="+source, "--local", "dockerfile="+work,
		"--frontend", "gateway.v0", "--opt", "source="+frontend,
		"--opt", "build-arg:cache-key="+name,
		"--import-cache", "type=registry,ref="+cache,
		"--export-cache", "type=registry,ref="+cache+",mode=max",
		"--output", "type=oci,dest="+filepath.Join(work, "image.tar")+",name="+image); err != nil {
		return "", err
	}
	ociDir := filepath.Join(work, "oci")
	if err := os.MkdirAll(ociDir, 0o700); err != nil {
		return "", err
	}
	if err := command(ctx, "tar", "-C", ociDir, "-xf", filepath.Join(work, "image.tar")); err != nil {
		return "", err
	}
	scanArchive := filepath.Join(work, "scan.tar")
	if err := command(ctx, "skopeo", "copy", "oci:"+ociDir, "docker-archive:"+scanArchive+":"+image); err != nil {
		return "", err
	}
	scanReport := filepath.Join(work, "trivy.json")
	if err := command(ctx, "trivy", "image", "--input", scanArchive, "--exit-code", "1", "--severity", "CRITICAL", "--ignore-unfixed", "--pkg-types", "library", "--scanners", "vuln", "--format", "json", "--output", scanReport); err != nil {
		if summary := summarizeTrivyFile(scanReport); summary != "" {
			return "", fmt.Errorf("%s", summary)
		}
		return "", err
	}
	if err := validateScanReport(scanReport); err != nil {
		return "", err
	}
	sbom := filepath.Join(work, "sbom.spdx.json")
	if err := command(ctx, "syft", "oci-dir:"+ociDir, "--output", "spdx-json="+sbom); err != nil {
		return "", err
	}
	info, err := os.Stat(sbom)
	if err != nil {
		return "", fmt.Errorf("Syft did not create an SBOM: %w", err)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("Syft created an empty SBOM")
	}

	if err := command(ctx, "skopeo", "copy", "--authfile", authFile, "oci:"+ociDir, "docker://"+image); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "skopeo", "inspect", "--authfile", authFile, "--format", "{{.Digest}}", "docker://"+image)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspect published image: %w", err)
	}
	digest := strings.TrimSpace(string(output))
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("registry returned invalid digest %q", digest)
	}
	fmt.Printf("published %s@%s (Trivy passed; Syft SBOM generated)\n", image, digest)
	return digest, nil
}

func validateScanReport(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Trivy report: %w", err)
	}
	var report struct {
		Results []struct {
			Target string `json:"Target"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("parse Trivy report: %w", err)
	}
	for _, result := range report.Results {
		if strings.TrimSpace(result.Target) != "" {
			return nil
		}
	}
	return fmt.Errorf("Trivy did not scan an image target")
}

func waitForBuildkit(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		cmd := exec.CommandContext(ctx, "buildctl", "--addr", "unix:///run/buildkit/buildkitd.sock", "debug", "workers")
		if err := cmd.Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("BuildKit did not become ready: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential %s: %w", path, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("credential %s is empty", path)
	}
	return token, nil
}

func command(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

const terminationLog = "/dev/termination-log"

// WriteTermination records a short failure reason for the Job (kubelet, 4KiB).
func WriteTermination(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	runes := []rune(msg)
	if len(runes) > 4096 {
		msg = string(runes[:4096])
	}
	_ = os.WriteFile(terminationLog, []byte(msg), 0o644)
}

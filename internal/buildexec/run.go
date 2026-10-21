package buildexec

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/buildsarchive"
	"git.host.bzh/pepe/kuberpack/internal/fail"
	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/git"
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
	RootDirectory     string
	BuildCommand      string
	GitTokenFile      string
	RegistryTokenFile string
	// GenerateLock tells the builder to resolve the Python dependencies when
	// the repository has no lock file.
	GenerateLock bool
	// GitAuthScheme is how the forge of the application authenticates a
	// clone: "token" or "basic".
	GitAuthScheme string
	// BuildsGitAuthScheme is how the forge of the builds repository
	// authenticates a clone: it is the forge of the platform, not the one
	// of the application.
	BuildsGitAuthScheme string
	BuildsGitURL        string
	AppName             string
	Environment         string
}

// The builder Secret is written by the operator (Infisical or kubectl). Its
// keys may be named either by their short role or by their Infisical key: the
// mounted file names are the Secret keys, so both spellings are accepted.
const (
	gitTokenPath         = "/secrets/git-token"
	gitTokenInfisicalKey = "/secrets/kuberpack-builder-git-token"
	registryTokenPath    = "/secrets/registry-token"
	registryTokenKey     = "/secrets/kuberpack-builder-registry-token"
	buildsTokenPath      = "/secrets/builds-token"
)

// Run builds one commit in a Kubernetes Job. BuildKit runs in a separate
// rootless sidecar; the resulting application image never contains these tools.
func Run(ctx context.Context, req Request) (digest string, err error) {
	var logBuf bytes.Buffer
	var sbom []byte
	defer func() {
		status, reason := "succeeded", ""
		if err != nil {
			status = "failed"
			reason = err.Error()
		}
		archiveBuild(ctx, req, sbom, status, reason, logBuf.String())
	}()
	if req.CloneURL == "" || req.ImageRepository == "" || req.RegistryUser == "" || !shaPattern.MatchString(req.CommitSHA) {
		return "", fail.Text(fail.Clone, "URL, SHA, repository and registry user are required")
	}
	host, name, err := oci.SplitRepository(req.ImageRepository)
	if err != nil {
		return "", fail.Stage(fail.Image, err)
	}
	gitToken, err := readFirstToken(gitTokenPaths(req.GitTokenFile)...)
	if err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	registryToken, err := readFirstToken(registryTokenPaths(req.RegistryTokenFile)...)
	if err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	work, err := os.MkdirTemp("/work", "build-*")
	if err != nil {
		return "", fail.Stage(fail.Job, err)
	}
	defer os.RemoveAll(work)
	authDir := filepath.Join(work, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	authFile := filepath.Join(authDir, "config.json")
	auth := base64.StdEncoding.EncodeToString([]byte(req.RegistryUser + ":" + registryToken))
	data, err := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	if err := os.WriteFile(authFile, data, 0o600); err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	if err := os.Setenv("DOCKER_CONFIG", authDir); err != nil {
		return "", fail.Stage(fail.Auth, err)
	}
	source := filepath.Join(work, "src")
	// The forge of the application has its own way to authenticate a clone:
	// a token in a header for a Gitea, the basic scheme for GitHub and
	// GitLab.
	authArgs := git.CloneAuthArgs(req.GitAuthScheme, gitToken)
	if err := fetch.Checkout(ctx, req.CloneURL, req.CommitSHA, source, authArgs); err != nil {
		return "", fail.Stage(fail.Clone, err)
	}
	fmt.Printf("checked out %s\n", req.CommitSHA)
	// root_directory selects the sub-directory Railpack analyzes and builds.
	buildDir := source
	if root := strings.Trim(strings.TrimSpace(req.RootDirectory), "/"); root != "" && root != "." {
		candidate := filepath.Join(source, filepath.FromSlash(root))
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.IsDir() {
			return "", fail.Text(fail.Strategy, "root_directory not found: "+root)
		}
		buildDir = candidate
	}
	// A Python project without a lock file: the build resolves the
	// dependencies. The lock of the repository is used when there is one.
	// A third party application cannot be asked for a lock.
	if req.GenerateLock {
		if err := resolvePythonLock(ctx, buildDir); err != nil {
			return "", fail.Stage(fail.Strategy, err)
		}
	}
	prepared, err := railpack.Prepare(ctx, railpack.Request{
		Dir:      buildDir,
		WorkDir:  work,
		StartCmd: req.StartCmd,
		BuildCmd: req.BuildCommand,
	})
	if err != nil {
		return "", fail.Stage(fail.Railpack, err)
	}
	// The compiled binary of the application lives in /app. The start
	// command finds it by its name: "miniflux" and not "./miniflux".
	if err := addAppPath(prepared.PlanPath); err != nil {
		return "", fail.Stage(fail.Railpack, err)
	}
	if err := waitForBuildkit(ctx, time.Minute); err != nil {
		return "", fail.Stage(fail.Buildkit, err)
	}

	tag := "sha-" + req.CommitSHA
	cache := req.ImageRepository + ":buildcache"
	image := req.ImageRepository + ":" + tag
	if err = commandLogged(ctx, &logBuf, "buildctl", "--addr", "unix:///run/buildkit/buildkitd.sock", "build",
		"--local", "context="+buildDir, "--local", "dockerfile="+work,
		"--frontend", "gateway.v0", "--opt", "source="+frontend,
		"--opt", "build-arg:cache-key="+name,
		"--import-cache", "type=registry,ref="+cache,
		"--export-cache", "type=registry,ref="+cache+",mode=max",
		"--output", "type=oci,dest="+filepath.Join(work, "image.tar")+",name="+image); err != nil {
		return "", fail.Stage(fail.Image, err)
	}
	ociDir := filepath.Join(work, "oci")
	if err := os.MkdirAll(ociDir, 0o700); err != nil {
		return "", fail.Stage(fail.Image, err)
	}
	if err = commandLogged(ctx, &logBuf, "tar", "-C", ociDir, "-xf", filepath.Join(work, "image.tar")); err != nil {
		return "", fail.Stage(fail.Image, err)
	}
	scanArchive := filepath.Join(work, "scan.tar")
	if err = commandLogged(ctx, &logBuf, "skopeo", "copy", "oci:"+ociDir, "docker-archive:"+scanArchive+":"+image); err != nil {
		return "", fail.Stage(fail.Trivy, err)
	}
	scanReport := filepath.Join(work, "trivy.json")
	if err = commandLogged(ctx, &logBuf, "trivy", "image", "--input", scanArchive, "--exit-code", "1", "--severity", "CRITICAL", "--ignore-unfixed", "--pkg-types", "library", "--scanners", "vuln", "--format", "json", "--output", scanReport); err != nil {
		if summary := summarizeTrivyFile(scanReport); summary != "" {
			return "", fail.Text(fail.Trivy, summary)
		}
		return "", fail.Stage(fail.Trivy, err)
	}
	if err := validateScanReport(scanReport); err != nil {
		return "", fail.Stage(fail.Trivy, err)
	}
	sbomPath := filepath.Join(work, "sbom.spdx.json")
	if err = commandLogged(ctx, &logBuf, "syft", "oci-dir:"+ociDir, "--output", "spdx-json="+sbomPath); err != nil {
		return "", fail.Stage(fail.SBOM, err)
	}
	info, err := os.Stat(sbomPath)
	if err != nil {
		return "", fail.Stage(fail.SBOM, err)
	}
	if info.Size() == 0 {
		return "", fail.Text(fail.SBOM, "empty report")
	}
	sbom, err = os.ReadFile(sbomPath)
	if err != nil {
		return "", fail.Stage(fail.SBOM, err)
	}

	if err = commandLogged(ctx, &logBuf, "skopeo", "copy", "--authfile", authFile, "oci:"+ociDir, "docker://"+image); err != nil {
		return "", fail.Stage(fail.Push, err)
	}
	cmd := exec.CommandContext(ctx, "skopeo", "inspect", "--authfile", authFile, "--format", "{{.Digest}}", "docker://"+image)
	output, err := cmd.Output()
	if err != nil {
		return "", fail.Stage(fail.Push, err)
	}
	digest = strings.TrimSpace(string(output))
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fail.Text(fail.Push, "invalid digest")
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

// addAppPath puts the directory of the application in the PATH of the
// image. The compiled binary of a Go, Rust or C application lives there: the
// start command names it, without a path.
func addAppPath(planPath string) error {
	if strings.TrimSpace(planPath) == "" {
		return nil
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read the plan: %w", err)
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("read the plan: %w", err)
	}
	deploy, ok := plan["deploy"].(map[string]any)
	if !ok {
		return nil
	}
	variables, ok := deploy["variables"].(map[string]any)
	if !ok {
		variables = map[string]any{}
		deploy["variables"] = variables
	}
	if _, exists := variables["PATH"]; !exists {
		variables["PATH"] = "/app:$PATH"
	}
	// The start command is run by a shell: it finds the binary of the
	// application in /app, whatever the name of the binary is.
	if command, ok := deploy["startCommand"].(string); ok && strings.TrimSpace(command) != "" {
		deploy["startCommand"] = "export PATH=/app:$PATH; " + command
	}
	fmt.Printf("start command %q, PATH of the application in the image\n", deploy["startCommand"])
	encoded, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("write the plan: %w", err)
	}
	return os.WriteFile(planPath, encoded, 0o600)
}

// resolvePythonLock resolves the Python dependencies of a project that has a
// pyproject.toml and no lock. The lock is written in the build context, not
// in the repository: Kuberpack never writes in the repository of the
// application.
func resolvePythonLock(ctx context.Context, dir string) error {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return fmt.Errorf("uv is not available to resolve the dependencies")
	}
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err != nil {
		return nil
	}
	fmt.Printf("resolving the Python dependencies (no lock in the repository)\n")
	cmd := exec.CommandContext(ctx, uv, "lock")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv lock: %s", strings.TrimSpace(string(out)))
	}
	return nil
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

// readFirstToken returns the first readable, non-empty credential among the
// candidates.
func readFirstToken(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("no credential path given")
	}
	for _, path := range paths {
		if token, err := readToken(path); err == nil {
			return token, nil
		}
	}
	return "", fmt.Errorf("no readable credential in %s", strings.Join(paths, ", "))
}

// gitTokenPaths lists where the app-clone token may be mounted.
func gitTokenPaths(explicit string) []string {
	out := make([]string, 0, 3)
	if p := strings.TrimSpace(explicit); p != "" {
		out = append(out, p)
	}
	return append(out, gitTokenPath, gitTokenInfisicalKey)
}

// registryTokenPaths lists where the registry token may be mounted.
func registryTokenPaths(explicit string) []string {
	out := make([]string, 0, 3)
	if p := strings.TrimSpace(explicit); p != "" {
		out = append(out, p)
	}
	return append(out, registryTokenPath, registryTokenKey)
}

func commandLogged(ctx context.Context, logBuf *bytes.Buffer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	writers := []io.Writer{os.Stdout, os.Stderr}
	if logBuf != nil {
		writers = append(writers, logBuf)
	}
	w := io.MultiWriter(writers...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func archiveBuild(ctx context.Context, req Request, sbom []byte, status, reason, excerpt string) {
	url := strings.TrimSpace(req.BuildsGitURL)
	if url == "" {
		url = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDS_GIT_URL"))
	}
	if url == "" {
		return
	}
	token := ""
	for _, path := range append([]string{buildsTokenPath}, gitTokenPaths(req.GitTokenFile)...) {
		if t, err := readToken(path); err == nil {
			token = t
			break
		}
	}
	app := req.AppName
	if app == "" {
		app = req.ImageRepository
	}
	env := req.Environment
	if env == "" {
		env = "production"
	}
	_, _ = buildsarchive.Push(ctx, buildsarchive.Request{
		AuthScheme:  req.BuildsGitAuthScheme,
		GitURL:      url,
		Token:       token,
		App:         app,
		CommitSHA:   req.CommitSHA,
		Environment: env,
		Status:      status,
		SBOM:        sbom,
		LogExcerpt:  excerpt,
		Reason:      reason,
	})
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

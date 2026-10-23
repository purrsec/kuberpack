package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/buildexec"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/promote"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "promote":
		if err := runPromote(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "trigger":
		if err := runTrigger(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "install":
		if err := runInstall(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "project":
		if err := runProject(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "apps":
		if err := runApps(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "app":
		if err := runApp(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "reloader":
		if err := runReloader(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "builder":
		if _, err := buildexec.Run(context.Background(), buildexec.Request{
			CloneURL:                os.Getenv("KUBERPACK_CLONE_URL"),
			CommitSHA:               os.Getenv("KUBERPACK_COMMIT_SHA"),
			ImageRepository:         os.Getenv("KUBERPACK_IMAGE_REPOSITORY"),
			RegistryUser:            os.Getenv("KUBERPACK_REGISTRY_USER"),
			StartCmd:                os.Getenv("KUBERPACK_START_CMD"),
			RootDirectory:           os.Getenv("KUBERPACK_ROOT_DIRECTORY"),
			BuildCommand:            os.Getenv("KUBERPACK_BUILD_CMD"),
			BuildsGitURL:            os.Getenv("KUBERPACK_BUILDS_GIT_URL"),
			AppName:                 os.Getenv("KUBERPACK_APP_NAME"),
			Environment:             os.Getenv("KUBERPACK_ENVIRONMENT"),
			GenerateLock:            os.Getenv("KUBERPACK_GENERATE_LOCK") == "1" || os.Getenv("KUBERPACK_GENERATE_LOCK") == "true",
			GitAuthScheme:           os.Getenv("KUBERPACK_GIT_AUTH"),
			CloneAnonymous:          os.Getenv("KUBERPACK_GIT_ANONYMOUS") == "true",
			BuildsGitAuthScheme:     os.Getenv("KUBERPACK_BUILDS_GIT_AUTH"),
			AcceptedVulnerabilities: splitList(os.Getenv("KUBERPACK_TRIVY_IGNORE")),
		}); err != nil {
			buildexec.WriteTermination(err.Error())
			fmt.Fprintf(os.Stderr, "kuberpack builder: %v\n", err)
			os.Exit(1)
		}
	case "-h", "-help", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// splitList reads a comma separated list of values.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func usage() {
	fmt.Fprintf(os.Stderr, `kuberpack: GitOps pin (promote), release trigger, control plane (serve), build Job worker (builder).

Usage:
  kuberpack promote [flags]
  kuberpack trigger [flags]
  kuberpack install --gitops-url URL --git-token TOKEN --registry-token TOKEN
  kuberpack serve [--addr :8080] [--data data]
  kuberpack project init|compile|create|delete [flags]
  kuberpack apps [--json]
  kuberpack app status [--json] <name>
  kuberpack app logs <name> [-f] [--tail N] [--since 10m] [--previous]
  kuberpack app builds|build-logs|redeploy [flags] <name>
  kuberpack doctor network --context NAME [--detect-only | --yes]
  kuberpack reloader          (restarts pods whose synchronised Secret changed)
  kuberpack builder           (inside a Kuberpack Kubernetes Job)

promote:
  --gitops-url string
  --gitops-branch string   (default main)
  --values-path string
  --chart string
  --image-repository string
  --commit-sha string
  --digest string

trigger (FORGEJO_TOKEN, write:repository):
  --forgejo-url string     (default FORGEJO_URL or https://git.host.bzh)
  --repository owner/name
  --branch string          (default main)
  --strategy auto|uv|railpack
  --start-cmd string       optional Railpack start command
  --gitops-url string      (default KUBERPACK_GITOPS_URL)
  --values-path string     (default <KUBERPACK_GITOPS_APPS>/<repository>/values.yaml)
  --chart string           (default charts/stateless)
  --wait duration          (default 15m)
  --skip-promote

  Submits a Kubernetes build Job (same as serve). Requires a cluster builder.

project (KUBERPACK_API_TOKEN, KUBERPACK_API_URL):
  init    --project name --repository owner/name --hostname host
          creates kuberpack.yaml locally; no API connection required
  compile [-f kuberpack.yaml] [--dry-run]
          sends the reviewed manifest to the control plane
  create  --project name [--namespace ns] [--service name] [--repository owner/name]
          [--port 8080] [--hostname host] [--dry-run]
  delete  --project name [--force]

  Compiles a project manifest into GitOps primitives.

doctor network:
  --context string        explicit Kubernetes context (required)
  --kubeconfig string     kubeconfig path (default loading rules)
  --detect-only           inspect the CNI and print manual commands; no writes
  --yes                   confirm the temporary namespace and probe pods
  --image string          busybox image reachable from the target cluster
  --timeout duration      probe timeout (default 3m)

  Refuses the implicit current context and requires --yes for any probe; a
  production-looking context is called out explicitly. The probe creates a
  target pod, client pods and a deny-all policy, observes real traffic, then
  deletes its temporary namespace. It never reads a Secret. See
  docs/network-matrix.md.

serve (FORGEJO_TOKEN, KUBERPACK_API_TOKEN, KUBERPACK_BUILDER_IMAGE):
  POST /api/v1/projects      compile a project manifest (Bearer)
  GET  /api/v1/projects      list
  GET  /api/v1/projects/{name}
  DELETE /api/v1/projects/{name}
  GET  /api/v1/apps          list
  GET  /api/v1/apps/{name}
  GET  /api/v1/apps/{name}/status
  GET  /api/v1/apps/{name}/logs
  GET  /api/v1/apps/{name}/builds
  POST /api/v1/apps/{name}/redeploy
  POST /hooks/forgejo        HMAC (push, pull_request)
  POST /hooks/flux           HelmRelease rollback → rolled_back
  GET  /healthz

  SQLite + HMAC files under --data. The manifest is the only way to declare
  an application: compiling it writes the GitOps Helm files, arms the build
  pipeline and poses the webhook. The promote only rewrites image (track
  stays human-owned) and enables Flux.
`)
}

func runPromote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	gitopsURL := fs.String("gitops-url", "", "GitOps repository URL or local path")
	gitopsBranch := fs.String("gitops-branch", "main", "GitOps branch (usually main)")
	valuesPath := fs.String("values-path", "", "values.yaml path inside the GitOps repository")
	chartPath := fs.String("chart", "", "path to the stateless Helm chart")
	imageRepository := fs.String("image-repository", "", "OCI repository without tag or digest")
	commitSHA := fs.String("commit-sha", "", "application commit SHA that was built")
	digest := fs.String("digest", "", "image digest (sha256:...)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ref, err := image.Pin(*imageRepository, *commitSHA, *digest)
	if err != nil {
		return err
	}

	result, err := promote.Run(context.Background(), promote.Request{
		GitOpsURL:    *gitopsURL,
		GitOpsBranch: *gitopsBranch,
		ValuesPath:   *valuesPath,
		ChartPath:    *chartPath,
		HTTPToken:    strings.TrimSpace(os.Getenv("FORGEJO_TOKEN")),
		AuthScheme:   strings.TrimSpace(os.Getenv("KUBERPACK_GIT_AUTH")),
		Image:        ref,
	})
	if err != nil {
		return err
	}
	if result.Changed {
		fmt.Printf("promoted %s to %s (%s)\n", result.App, result.Image, result.InfraCommitSHA)
		return nil
	}
	fmt.Printf("%s already at %s (%s)\n", result.App, result.Image, result.InfraCommitSHA)
	return nil
}

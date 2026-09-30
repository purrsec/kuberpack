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
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kuberpack: %v\n", err)
			os.Exit(1)
		}
	case "builder":
		if _, err := buildexec.Run(context.Background(), buildexec.Request{
			CloneURL:        os.Getenv("KUBERPACK_CLONE_URL"),
			CommitSHA:       os.Getenv("KUBERPACK_COMMIT_SHA"),
			ImageRepository: os.Getenv("KUBERPACK_IMAGE_REPOSITORY"),
			RegistryUser:    os.Getenv("KUBERPACK_REGISTRY_USER"),
			StartCmd:        os.Getenv("KUBERPACK_START_CMD"),
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

func usage() {
	fmt.Fprintf(os.Stderr, `kuberpack: GitOps pin (promote), release trigger, control plane (serve), build Job worker (builder).

Usage:
  kuberpack promote [flags]
  kuberpack trigger [flags]
  kuberpack serve [--addr :8080] [--data data]
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
  --values-path string     (default kubernetes/vps/apps/<name>/values.yaml)
  --chart string           (default charts/stateless)
  --wait duration          (default 15m)
  --skip-promote

  Submits a Kubernetes build Job (same as serve). Requires a cluster builder.

serve (FORGEJO_TOKEN, KUBERPACK_API_TOKEN, KUBERPACK_BUILDER_IMAGE):
  POST /api/v1/apps          register (Bearer)
  GET  /api/v1/apps[/{name}]
  POST /hooks/forgejo        HMAC (X-Gitea-Signature)
  GET  /healthz

  SQLite + HMAC files under --data. Register writes GitOps Helm files;
  promote only rewrites image (track stays human-owned) and enables Flux.
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

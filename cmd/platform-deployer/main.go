package main

import (
	"context"
	"flag"
	"fmt"
	"os"

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
			fmt.Fprintf(os.Stderr, "platform-deployer: %v\n", err)
			os.Exit(1)
		}
	case "trigger":
		if err := runTrigger(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "platform-deployer: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "platform-deployer: %v\n", err)
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
	fmt.Fprintf(os.Stderr, `platform-deployer: GitOps pin (promote), Forgejo dispatch (trigger), healthz (serve).

Usage:
  platform-deployer promote [flags]
  platform-deployer trigger [flags]
  platform-deployer serve [--addr :8080]

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

  Clones the SHA, runs railpack prepare, dispatches pepe/infra-homelab
  app-release.yaml. Does not promote. uv has no executor yet: auto|uv still
  dispatch Railpack after uv.lock --check.
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

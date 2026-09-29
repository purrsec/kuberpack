package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/railpack"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

func runTrigger(args []string) error {
	fs := flag.NewFlagSet("trigger", flag.ContinueOnError)
	forgejoURL := fs.String("forgejo-url", getenv("FORGEJO_URL", "https://git.host.bzh"), "Forgejo base URL")
	repository := fs.String("repository", "", "Forgejo repository owner/name")
	branch := fs.String("branch", "main", "production branch")
	strategyName := fs.String("strategy", "auto", "auto, uv, or railpack")
	startCmd := fs.String("start-cmd", "", "optional Railpack start command")
	if err := fs.Parse(args); err != nil {
		return err
	}

	owner, name, err := forgejo.ParseOwnerName(*repository)
	if err != nil {
		return err
	}
	kind, err := strategy.ParseKind(*strategyName)
	if err != nil {
		return err
	}

	token := strings.TrimSpace(os.Getenv("FORGEJO_TOKEN"))
	client, err := forgejo.New(*forgejoURL, token)
	if err != nil {
		return err
	}

	ctx := context.Background()
	repo, err := client.Repo(ctx, owner, name)
	if err != nil {
		return err
	}
	sha, err := client.BranchSHA(ctx, owner, name, *branch)
	if err != nil {
		return err
	}

	parent, err := os.MkdirTemp("", "kuberpack-trigger-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	dest := filepath.Join(parent, "src")

	if err := fetch.Checkout(ctx, repo.CloneURL, sha, dest, fetch.TokenHeaderArgs(token)); err != nil {
		return err
	}
	plan, err := strategy.Resolve(dest, kind)
	if err != nil {
		return err
	}

	fmt.Printf("repository: %s\n", repo.FullName)
	fmt.Printf("branch: %s\n", *branch)
	fmt.Printf("sha: %s\n", sha)
	fmt.Printf("strategy: %s\n", plan.Kind)
	fmt.Printf("clone: ok\n")

	if plan.Kind == strategy.UV {
		if err := strategy.CheckLock(dest); err != nil {
			return err
		}
		fmt.Println("uv executor not implemented; building with railpack")
		plan.Kind = strategy.Railpack
	}

	switch plan.Kind {
	case strategy.Railpack:
		prep, err := railpack.Prepare(ctx, railpack.Request{
			Dir:      dest,
			WorkDir:  filepath.Join(parent, "railpack"),
			StartCmd: *startCmd,
		})
		if err != nil {
			return err
		}
		fmt.Printf("railpack prepare: ok\n")
		fmt.Printf("frontend: %s\n", prep.Frontend)

		dispatch := getenv("KUBERPACK_DISPATCH_REPO", "pepe/infra-homelab")
		workflow := getenv("KUBERPACK_DISPATCH_WORKFLOW", "app-release.yaml")
		dOwner, dName, err := forgejo.ParseOwnerName(dispatch)
		if err != nil {
			return err
		}
		if err := client.DispatchWorkflow(ctx, dOwner, dName, workflow, "main", map[string]string{
			"repository": repo.FullName,
			"sha":        sha,
		}); err != nil {
			return fmt.Errorf("dispatch %s %s: %w (FORGEJO_TOKEN needs write:repository)", dispatch, workflow, err)
		}
		fmt.Printf("dispatched %s %s on app-builder\n", dispatch, workflow)
		fmt.Println("not promoting")
	default:
		fmt.Println("no builder configured; not promoting")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

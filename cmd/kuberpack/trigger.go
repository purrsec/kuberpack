package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

func runTrigger(args []string) error {
	fs := flag.NewFlagSet("trigger", flag.ContinueOnError)
	forgejoURL := fs.String("forgejo-url", getenv("FORGEJO_URL", "https://git.host.bzh"), "Forgejo base URL")
	repository := fs.String("repository", "", "Forgejo repository owner/name")
	branch := fs.String("branch", "main", "production branch")
	strategyName := fs.String("strategy", "auto", "auto, uv, or railpack")
	startCmd := fs.String("start-cmd", "", "optional Railpack start command")
	gitopsURL := fs.String("gitops-url", getenv("KUBERPACK_GITOPS_URL", "https://git.host.bzh/pepe/infra-homelab.git"), "GitOps repository URL")
	gitopsBranch := fs.String("gitops-branch", getenv("KUBERPACK_GITOPS_BRANCH", "main"), "GitOps branch")
	valuesPath := fs.String("values-path", getenv("KUBERPACK_VALUES_PATH", ""), "values.yaml path inside the GitOps repository")
	chartPath := fs.String("chart", getenv("KUBERPACK_CHART", "charts/stateless"), "path to the stateless Helm chart")
	imageRepo := fs.String("image-repository", getenv("KUBERPACK_IMAGE_REPOSITORY", ""), "OCI repository without tag or digest")
	waitFor := fs.Duration("wait", 15*time.Minute, "how long to wait for the image digest")
	skipPromote := fs.Bool("skip-promote", false, "dispatch and wait, but do not write GitOps")
	if err := fs.Parse(args); err != nil {
		return err
	}

	owner, name, err := forgejo.ParseOwnerName(*repository)
	if err != nil {
		return err
	}
	if _, err := strategy.ParseKind(*strategyName); err != nil {
		return err
	}

	token := strings.TrimSpace(os.Getenv("FORGEJO_TOKEN"))
	client, err := forgejo.New(*forgejoURL, token)
	if err != nil {
		return err
	}

	_, err = release.Run(context.Background(), release.Request{
		Client:           client,
		Token:            token,
		Owner:            owner,
		Name:             name,
		Branch:           *branch,
		Strategy:         *strategyName,
		StartCmd:         *startCmd,
		DispatchRepo:     getenv("KUBERPACK_DISPATCH_REPO", "pepe/infra-homelab"),
		DispatchWorkflow: getenv("KUBERPACK_DISPATCH_WORKFLOW", "app-release.yaml"),
		GitOpsURL:        *gitopsURL,
		GitOpsBranch:     *gitopsBranch,
		ValuesPath:       *valuesPath,
		ChartPath:        *chartPath,
		ImageRepository:  *imageRepo,
		RegistryUser:     getenv("FORGEJO_REGISTRY_USER", owner),
		Wait:             *waitFor,
		SkipPromote:      *skipPromote,
		Log:              func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	return err
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

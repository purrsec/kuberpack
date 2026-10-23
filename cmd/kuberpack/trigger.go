package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/netguard"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

func runTrigger(args []string) error {
	fs := flag.NewFlagSet("trigger", flag.ContinueOnError)
	gitProvider := fs.String("git-provider", getenv("KUBERPACK_GIT_PROVIDER", "forgejo"), "git forge: forgejo, github or gitlab")
	gitURL := fs.String("git-url", getenv("KUBERPACK_GIT_URL", getenv("FORGEJO_URL", "https://git.host.bzh")), "base URL of the git forge")
	registryURL := fs.String("registry-url", getenv("KUBERPACK_REGISTRY_URL", ""), "OCI registry the images are pushed to (defaults to the forge URL)")
	repository := fs.String("repository", "", "Forgejo repository owner/name")
	branch := fs.String("branch", "main", "production branch")
	strategyName := fs.String("strategy", "auto", "auto, uv, or railpack")
	startCmd := fs.String("start-cmd", "", "optional Railpack start command")
	gitopsURL := fs.String("gitops-url", getenv("KUBERPACK_GITOPS_URL", "https://git.host.bzh/pepe/infra-homelab.git"), "GitOps repository URL")
	gitopsBranch := fs.String("gitops-branch", getenv("KUBERPACK_GITOPS_BRANCH", "main"), "GitOps branch")
	valuesPath := fs.String("values-path", getenv("KUBERPACK_VALUES_PATH", ""), "values.yaml path inside the GitOps repository")
	chartPath := fs.String("chart", getenv("KUBERPACK_CHART", "provisioning/charts/stateless"), "path to the stateless Helm chart")
	imageRepo := fs.String("image-repository", getenv("KUBERPACK_IMAGE_REPOSITORY", ""), "OCI repository without tag or digest")
	waitFor := fs.Duration("wait", 15*time.Minute, "how long to wait for the image digest")
	skipPromote := fs.Bool("skip-promote", false, "dispatch and wait, but do not write GitOps")
	if err := fs.Parse(args); err != nil {
		return err
	}

	owner, name, err := git.SplitOwnerName(*repository)
	if err != nil {
		return err
	}
	if _, err := strategy.ParseKind(*strategyName); err != nil {
		return err
	}

	token := strings.TrimSpace(getenv("KUBERPACK_GIT_TOKEN", os.Getenv("FORGEJO_TOKEN")))
	provider, err := git.New(git.Config{Kind: *gitProvider, URL: *gitURL, Token: token})
	if err != nil {
		return err
	}

	egress := netguard.FromEnvironment().TrustURLs(*gitURL, *registryURL, *gitopsURL)
	_, err = release.Run(context.Background(), release.Request{
		Provider:        provider,
		RegistryURL:     *registryURL,
		RegistryToken:   strings.TrimSpace(os.Getenv("KUBERPACK_REGISTRY_TOKEN")),
		Token:           token,
		Owner:           owner,
		Name:            name,
		Branch:          *branch,
		Strategy:        *strategyName,
		StartCmd:        *startCmd,
		GitOpsURL:       *gitopsURL,
		GitOpsBranch:    *gitopsBranch,
		ValuesPath:      *valuesPath,
		ChartPath:       *chartPath,
		ImageRepository: *imageRepo,
		RegistryUser:    getenv("FORGEJO_REGISTRY_USER", owner),
		Wait:            *waitFor,
		SkipPromote:     *skipPromote,
		Platform:        platformFromEnv(),
		Egress:          egress,
		EgressAllow:     strings.TrimSpace(os.Getenv(netguard.EnvAllow)),
		Log:             func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	return err
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

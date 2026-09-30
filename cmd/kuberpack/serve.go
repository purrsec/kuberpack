package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/control"
	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", getenv("KUBERPACK_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data", getenv("KUBERPACK_DATA", "data"), "SQLite and HMAC secrets directory")
	forgejoURL := fs.String("forgejo-url", getenv("FORGEJO_URL", "https://git.host.bzh"), "Forgejo base URL")
	webhookURL := fs.String("webhook-url", getenv("KUBERPACK_WEBHOOK_URL", ""), "public URL for POST /hooks/forgejo")
	gitopsURL := fs.String("gitops-url", getenv("KUBERPACK_GITOPS_URL", "https://git.host.bzh/pepe/infra-homelab.git"), "GitOps repository URL")
	gitopsBranch := fs.String("gitops-branch", getenv("KUBERPACK_GITOPS_BRANCH", "main"), "GitOps branch")
	chartPath := fs.String("chart", getenv("KUBERPACK_CHART", "charts/stateless"), "path to the stateless Helm chart")
	waitFor := fs.Duration("wait", 15*time.Minute, "how long to wait for the image digest")
	if err := fs.Parse(args); err != nil {
		return err
	}

	token := strings.TrimSpace(os.Getenv("FORGEJO_TOKEN"))
	apiToken := getenv("KUBERPACK_API_TOKEN", token)
	client, err := forgejo.New(*forgejoURL, token)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(*dataDir, "kuberpack.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	var jobBuilder builder.Runner
	if image := strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_IMAGE")); image != "" {
		runner, err := builder.InCluster(image)
		if err != nil {
			return err
		}
		runner.Namespace = getenv("KUBERPACK_BUILDER_NAMESPACE", "kuberpack-build")
		runner.SecretName = getenv("KUBERPACK_BUILDER_SECRET", "kuberpack-builder")
		runner.PullSecretName = getenv("KUBERPACK_BUILDER_PULL_SECRET", "kuberpack-builder-pull")
		runner.Timeout = *waitFor
		jobBuilder = runner
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := control.New(control.Config{
		Builder:          jobBuilder,
		Store:            db,
		Secrets:          store.SecretsDir(filepath.Join(*dataDir, "secrets")),
		Client:           client,
		Token:            token,
		APIToken:         apiToken,
		WebhookURL:       strings.TrimSpace(*webhookURL),
		GitOpsURL:        *gitopsURL,
		GitOpsBranch:     *gitopsBranch,
		ChartPath:        *chartPath,
		DispatchRepo:     getenv("KUBERPACK_DISPATCH_REPO", "pepe/infra-homelab"),
		DispatchWorkflow: getenv("KUBERPACK_DISPATCH_WORKFLOW", "app-release.yaml"),
		Wait:             *waitFor,
	})
	srv.Start(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("kuberpack listening on %s\n", *addr)
	return httpSrv.ListenAndServe()
}

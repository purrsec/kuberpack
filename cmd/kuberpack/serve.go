package main

import (
	"log"

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
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"k8s.io/client-go/kubernetes"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", getenv("KUBERPACK_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data", getenv("KUBERPACK_DATA", "data"), "SQLite and HMAC secrets directory")
	gitProvider := fs.String("git-provider", getenv("KUBERPACK_GIT_PROVIDER", "forgejo"), "git forge: forgejo, github or gitlab")
	gitURL := fs.String("git-url", getenv("KUBERPACK_GIT_URL", getenv("FORGEJO_URL", "https://git.host.bzh")), "base URL of the git forge")
	registryURL := fs.String("registry-url", getenv("KUBERPACK_REGISTRY_URL", ""), "OCI registry the images are pushed to (defaults to the forge URL)")
	webhookURL := fs.String("webhook-url", getenv("KUBERPACK_WEBHOOK_URL", ""), "public URL for POST /hooks/forgejo")
	gitopsURL := fs.String("gitops-url", getenv("KUBERPACK_GITOPS_URL", "https://git.host.bzh/pepe/infra-homelab.git"), "GitOps repository URL")
	gitopsBranch := fs.String("gitops-branch", getenv("KUBERPACK_GITOPS_BRANCH", "main"), "GitOps branch")
	chartPath := fs.String("chart", getenv("KUBERPACK_CHART", "charts/stateless"), "path to the stateless Helm chart")
	waitFor := fs.Duration("wait", 15*time.Minute, "how long to wait for the image digest")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// The read token opens the repositories. The build token pushes the
	// images: the builder reads them from their own secrets.
	token := strings.TrimSpace(getenv("KUBERPACK_GIT_TOKEN", os.Getenv("FORGEJO_TOKEN")))
	apiToken := getenv("KUBERPACK_API_TOKEN", token)
	provider, err := git.New(git.Config{
		Kind:  *gitProvider,
		URL:   *gitURL,
		Token: token,
	})
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
		// The bounds of the builder follow the node it runs on.
		runner.NodeMemory = getenv("KUBERPACK_NODE_MEMORY", "")
		runner.SecretName = getenv("KUBERPACK_BUILDER_SECRET", "kuberpack-builder")
		runner.PullSecretName = getenv("KUBERPACK_BUILDER_PULL_SECRET", "kuberpack-builder-pull")
		runner.BuildkitImage = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_IMAGE"))
		runner.BuildsGitURL = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDS_GIT_URL"))
		runner.BuilderCPU = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_CPU"))
		runner.BuilderMemory = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_MEMORY"))
		runner.BuildkitCPU = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_CPU"))
		runner.BuildkitMemory = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_MEMORY"))
		runner.AcceptedVulnerabilities = splitList(os.Getenv("KUBERPACK_TRIVY_IGNORE"))
		runner.Timeout = *waitFor
		jobBuilder = runner
	}
	if jobBuilder == nil {
		return fmt.Errorf("KUBERPACK_BUILDER_IMAGE is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The platform writes in the cluster the secret that pulls the images of
	// its registry: a credential never goes to a Git repository.
	kubeClient, err := clusterClient()
	if err != nil {
		log.Printf("kuberpack: the cluster is not reachable: %v", err)
	}

	srv := control.New(control.Config{
		Kube:             kubeClient,
		BuilderNamespace: getenv("KUBERPACK_BUILDER_NAMESPACE", "kuberpack-build"),
		Builder:          jobBuilder,
		Store:            db,
		Secrets:          store.SecretsDir(filepath.Join(*dataDir, "secrets")),
		Provider:         provider,
		RegistryURL:      *registryURL,
		RegistryUser:     strings.TrimSpace(os.Getenv("KUBERPACK_REGISTRY_USER")),
		Token:            token,
		APIToken:         apiToken,
		WebhookURL:       strings.TrimSpace(*webhookURL),
		GitOpsURL:        *gitopsURL,
		GitOpsBranch:     *gitopsBranch,
		ChartPath:        *chartPath,
		Platform:         platformFromEnv(),
		Wait:             *waitFor,
		BuildsGitURL:     strings.TrimSpace(os.Getenv("KUBERPACK_BUILDS_GIT_URL")),
		FluxSecret:       strings.TrimSpace(os.Getenv("KUBERPACK_FLUX_WEBHOOK_SECRET")),
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

// clusterClient builds the client of the cluster the control plane runs in.
func clusterClient() (kubernetes.Interface, error) {
	config, err := kubeConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func platformFromEnv() promote.Platform {
	return promote.Platform{
		AppsDir:                getenv("KUBERPACK_GITOPS_APPS", "kubernetes/vps/apps"),
		ChartRef:               getenv("KUBERPACK_GITOPS_CHART", "./kubernetes/vps/charts/stateless"),
		ChartSourceKind:        getenv("KUBERPACK_GITOPS_CHART_SOURCE_KIND", "GitRepository"),
		SourceName:             getenv("KUBERPACK_GITOPS_SOURCE", "infra-homelab"),
		SourceNamespace:        getenv("KUBERPACK_GITOPS_SOURCE_NAMESPACE", "flux-system"),
		ReleaseNamespace:       getenv("KUBERPACK_RELEASE_NAMESPACE", "apps"),
		IngressClassName:       getenv("KUBERPACK_INGRESS_CLASS", "traefik"),
		ImagePullSecret:        getenv("KUBERPACK_IMAGE_PULL_SECRET", "forgejo-registry-pull"),
		IngressTLS:             getenvBool("KUBERPACK_INGRESS_TLS", true),
		ExternalDNSTarget:      promote.IPv6DNSTargets(os.Getenv("KUBERPACK_DNS_TARGET")),
		ExternalDNSTTL:         getenv("KUBERPACK_DNS_TTL", "300"),
		TraefikEntrypoint:      getenv("KUBERPACK_TRAEFIK_ENTRYPOINT", "websecure"),
		TraefikCertResolver:    getenv("KUBERPACK_CERT_RESOLVER", "letsencrypt"),
		NetworkPolicy:          getenvBool("KUBERPACK_NETWORK_POLICY", true),
		PreviewDomain:          getenv("KUBERPACK_PREVIEW_DOMAIN", "preview.host.bzh"),
		InfisicalAuthRef:       getenv("KUBERPACK_INFISICAL_AUTH_REF", "contabo-vps"),
		InfisicalProjectID:     strings.TrimSpace(os.Getenv("KUBERPACK_INFISICAL_PROJECT_ID")),
		InfisicalPath:          getenv("KUBERPACK_INFISICAL_PATH", "/"),
		InfisicalDir:           getenv("KUBERPACK_INFISICAL_DIR", "kubernetes/vps/infisical"),
		InfisicalNamespace:     getenv("KUBERPACK_INFISICAL_NAMESPACE", "infisical"),
		PostgresImage:          getenv("KUBERPACK_POSTGRES_IMAGE", ""),
		AddonOperators:         splitList(getenv("KUBERPACK_ADDON_OPERATORS", "")),
		NodeMemory:             getenv("KUBERPACK_NODE_MEMORY", ""),
		PostgresStorage:        getenv("KUBERPACK_POSTGRES_STORAGE", ""),
		PostgresStorageClass:   getenv("KUBERPACK_POSTGRES_STORAGE_CLASS", ""),
		PreviewDatabaseImage:   getenv("KUBERPACK_PREVIEW_DB_IMAGE", ""),
		PreviewDatabaseStorage: getenv("KUBERPACK_PREVIEW_DB_STORAGE", ""),
		PreviewDatabaseClass:   getenv("KUBERPACK_PREVIEW_DB_CLASS", ""),
		ProjectsDir:            getenv("KUBERPACK_PROJECTS_DIR", "kubernetes/vps/projects"),
		ControlPlaneNamespace:  getenv("KUBERPACK_CONTROL_PLANE_NAMESPACE", "kuberpack"),
	}
}

func getenvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

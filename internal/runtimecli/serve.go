package runtimecli

import (
	"encoding/json"
	"log"
	"strconv"

	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/control"
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/netguard"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
	"k8s.io/client-go/kubernetes"
)

func runServe(args []string) error {
	if raw := os.Getenv("KUBERPACK_INGRESS_POD_SELECTOR"); raw != "" {
		var selector map[string]string
		if err := json.Unmarshal([]byte(raw), &selector); err != nil || len(selector) == 0 {
			return fmt.Errorf("KUBERPACK_INGRESS_POD_SELECTOR must be a nonempty JSON label map")
		}
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", getenv("KUBERPACK_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data", getenv("KUBERPACK_DATA", "data"), "SQLite and HMAC secrets directory")
	gitProvider := fs.String("git-provider", getenv("KUBERPACK_GIT_PROVIDER", "forgejo"), "git forge: forgejo, github or gitlab")
	gitURL := fs.String("git-url", getenv("KUBERPACK_GIT_URL", getenv("FORGEJO_URL", "https://git.host.bzh")), "base URL of the git forge")
	registryURL := fs.String("registry-url", getenv("KUBERPACK_REGISTRY_URL", ""), "OCI registry the images are pushed to (defaults to the forge URL)")
	webhookURL := fs.String("webhook-url", getenv("KUBERPACK_WEBHOOK_URL", ""), "public URL for POST /hooks/forgejo")
	gitopsURL := fs.String("gitops-url", getenv("KUBERPACK_GITOPS_URL", "https://git.host.bzh/pepe/infra-homelab.git"), "GitOps repository URL")
	gitopsBranch := fs.String("gitops-branch", getenv("KUBERPACK_GITOPS_BRANCH", "main"), "GitOps branch")
	chartPath := fs.String("chart", getenv("KUBERPACK_CHART", "provisioning/charts/stateless"), "path to the stateless Helm chart")
	waitFor := fs.Duration("wait", 15*time.Minute, "how long to wait for the image digest")
	egressAllow := fs.String("egress-allow", os.Getenv(netguard.EnvAllow), "hôtes, IP ou CIDR autorisés pour les accès sortants (séparés par des virgules)")
	apiRateLimit := fs.Int("api-rate-limit", getenvInt("KUBERPACK_API_RATE_LIMIT", control.DefaultAPIRateLimit), "requêtes API par minute et par client (négatif désactive)")
	apiRateBurst := fs.Int("api-rate-burst", getenvInt("KUBERPACK_API_RATE_BURST", 0), "rafale autorisée par client (0 = quart de la limite)")
	apiMaxConcurrent := fs.Int("api-max-concurrency", getenvInt("KUBERPACK_API_MAX_CONCURRENCY", control.DefaultAPIMaxConcurrent), "requêtes simultanées (négatif désactive)")
	trustedProxies := fs.String("trusted-proxies", getenv("KUBERPACK_TRUSTED_PROXIES", ""), "CIDR des proxys dont X-Forwarded-For est lu")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected control plane arguments: %v", fs.Args())
	}

	// La politique d'egress refuse par défaut les adresses internes. Les
	// adresses configurées par l'opérateur (forge, registre, GitOps) sont de
	// confiance : une installation interne n'a pas à les déclarer deux fois.
	egress, err := netguard.Parse(splitList(*egressAllow))
	if err != nil {
		return fmt.Errorf("egress-allow: %w", err)
	}
	egress = egress.TrustURLs(*gitURL, *registryURL, *gitopsURL, os.Getenv("KUBERPACK_BUILDS_GIT_URL"))
	// Le builder clone le dépôt de l'application : il reçoit la même
	// allowlist, augmentée des adresses configurées par l'opérateur.
	builderEgressAllow := strings.Join(append(splitList(*egressAllow),
		splitList(configuredEgressHosts(*gitURL, *registryURL, *gitopsURL))...), ",")

	// The read token opens the repositories. The build token pushes the
	// images: the builder reads them from their own secrets.
	token := strings.TrimSpace(getenv("KUBERPACK_GIT_TOKEN", os.Getenv("FORGEJO_TOKEN")))
	// Missing API credentials close administrative routes. A Git credential
	// must never implicitly become a remotely usable administrator token.
	apiToken := strings.TrimSpace(os.Getenv("KUBERPACK_API_TOKEN"))
	if apiToken != "" && apiToken == token {
		return fmt.Errorf("KUBERPACK_API_TOKEN must be distinct from KUBERPACK_GIT_TOKEN")
	}
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
		// The size of the disk of the node: the volumes of a build
		// follow it, so that a build never fills the node.
		runner.NodeDisk = getenv("KUBERPACK_NODE_DISK", "")
		runner.SecretName = getenv("KUBERPACK_BUILDER_SECRET", "kuberpack-builder")
		runner.PullSecretName = getenv("KUBERPACK_BUILDER_PULL_SECRET", "kuberpack-builder-pull")
		runner.BuildkitImage = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_IMAGE"))
		runner.BuildsGitURL = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDS_GIT_URL"))
		runner.BuilderCPU = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_CPU"))
		runner.BuilderMemory = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_MEMORY"))
		runner.BuildkitCPU = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_CPU"))
		runner.BuildkitMemory = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_MEMORY"))
		runner.BuildkitCache = strings.TrimSpace(os.Getenv("KUBERPACK_BUILDKIT_CACHE"))
		// Le placement des builds est optionnel. Une valeur fournie est
		// validée ici : le control plane refuse de démarrer plutôt que de
		// créer des Jobs sur un nœud ou un runtime non prévus.
		runtimeClass := strings.TrimSpace(os.Getenv("KUBERPACK_BUILDER_RUNTIME_CLASS"))
		if err := builder.ValidateRuntimeClass(runtimeClass); err != nil {
			return fmt.Errorf("KUBERPACK_BUILDER_RUNTIME_CLASS: %w", err)
		}
		runner.RuntimeClass = runtimeClass
		selector, err := builder.ParseNodeSelector(os.Getenv("KUBERPACK_BUILDER_NODE_SELECTOR"))
		if err != nil {
			return fmt.Errorf("KUBERPACK_BUILDER_NODE_SELECTOR: %w", err)
		}
		runner.NodeSelector = selector
		tolerations, err := builder.ParseTolerations(os.Getenv("KUBERPACK_BUILDER_TOLERATIONS"))
		if err != nil {
			return fmt.Errorf("KUBERPACK_BUILDER_TOLERATIONS: %w", err)
		}
		runner.Tolerations = tolerations
		runner.AcceptedVulnerabilities = splitList(os.Getenv("KUBERPACK_TRIVY_IGNORE"))
		runner.Timeout = *waitFor
		jobBuilder = runner
	}
	if jobBuilder == nil {
		return fmt.Errorf("KUBERPACK_BUILDER_IMAGE is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// The platform writes in the cluster the secret that pulls the images of
	// its registry: a credential never goes to a Git repository.
	kubeConfig, err := kubeConfig()
	if err != nil {
		log.Printf("kuberpack: the cluster is not reachable: %v", err)
	}
	var kubeClient kubernetes.Interface
	if kubeConfig != nil {
		kubeClient, err = kubernetes.NewForConfig(kubeConfig)
		if err != nil {
			log.Printf("kuberpack: the cluster is not reachable: %v", err)
		}
	}

	platform := platformFromEnv()
	platform.Egress = egress
	srv := control.New(control.Config{
		Kube:             kubeClient,
		KubeConfig:       kubeConfig,
		KubeletCA:        []byte(os.Getenv("KUBERPACK_KUBELET_CA_PEM")),
		BuilderNamespace: getenv("KUBERPACK_BUILDER_NAMESPACE", "kuberpack-build"),
		// The volume of the cache of the builder: the database of the
		// vulnerabilities of Trivy. The platform creates it.
		BuilderCacheName:         getenv("KUBERPACK_BUILDER_CACHE_NAME", "kuberpack-builder-cache"),
		BuilderCacheSize:         getenv("KUBERPACK_BUILDER_CACHE_SIZE", "2Gi"),
		BuilderCacheStorageClass: getenv("KUBERPACK_STORAGE_CLASS", ""),
		Builder:                  jobBuilder,
		Store:                    db,
		Secrets:                  store.SecretsDir(filepath.Join(*dataDir, "secrets")),
		Provider:                 provider,
		RegistryURL:              *registryURL,
		RegistryUser:             strings.TrimSpace(os.Getenv("KUBERPACK_REGISTRY_USER")),
		RegistryToken:            strings.TrimSpace(os.Getenv("KUBERPACK_REGISTRY_TOKEN")),
		Token:                    token,
		APIToken:                 apiToken,
		WebhookURL:               strings.TrimSpace(*webhookURL),
		GitOpsURL:                *gitopsURL,
		GitOpsBranch:             *gitopsBranch,
		AppsSource:               getenv("KUBERPACK_APPS_SOURCE", "kuberpack-gitops"),
		TenantIsolation:          getenvBool("KUBERPACK_TENANT_ISOLATION", false),
		Reloader:                 getenvBool("KUBERPACK_RELOADER", false),
		ChartPath:                *chartPath,
		WriteCharts:              getenvBool("KUBERPACK_WRITE_CHARTS", true),
		// The chart of the control plane itself. The platform writes it in
		// the GitOps repository: the deployment of the platform follows
		// the image that runs it.
		SelfChartPath:    getenv("KUBERPACK_SELF_CHART", "/opt/kuberpack/charts/kuberpack"),
		SelfChartRef:     getenv("KUBERPACK_SELF_CHART_REF", "kubernetes/vps/charts/kuberpack"),
		Platform:         platform,
		Egress:           egress,
		EgressAllow:      builderEgressAllow,
		APIRateLimit:     *apiRateLimit,
		APIRateBurst:     *apiRateBurst,
		APIMaxConcurrent: *apiMaxConcurrent,
		TrustedProxies:   splitList(*trustedProxies),
		Wait:             *waitFor,
		BuildsGitURL:     strings.TrimSpace(os.Getenv("KUBERPACK_BUILDS_GIT_URL")),
		FluxSecret:       strings.TrimSpace(os.Getenv("KUBERPACK_FLUX_WEBHOOK_SECRET")),
	})
	srv.Start(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
		// Runtime log follow responses intentionally stay open. Their writer
		// sets a fresh deadline for each write to evict stalled consumers.
	}
	fmt.Printf("kuberpack listening on %s\n", *addr)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()
	err = httpSrv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
		IngressNamespace:       getenv("KUBERPACK_INGRESS_NAMESPACE", "kube-system"),
		IngressPodSelector:     ingressPodSelectorFromEnv(),
		Issuer:                 getenv("KUBERPACK_ISSUER", "letsencrypt"),
		VolumeStorageClass:     getenv("KUBERPACK_STORAGE_CLASS", ""),
		ImagePullSecret:        getenv("KUBERPACK_IMAGE_PULL_SECRET", "forgejo-registry-pull"),
		IngressTLS:             getenvBool("KUBERPACK_INGRESS_TLS", true),
		ExternalDNSTarget:      promote.IPv6DNSTargets(os.Getenv("KUBERPACK_DNS_TARGET")),
		ExternalDNSTTL:         getenv("KUBERPACK_DNS_TTL", "300"),
		TraefikEntrypoint:      getenv("KUBERPACK_TRAEFIK_ENTRYPOINT", "websecure"),
		TraefikCertResolver:    getenv("KUBERPACK_CERT_RESOLVER", "letsencrypt"),
		NetworkPolicy:          getenvBool("KUBERPACK_NETWORK_POLICY", true),
		TenantIsolation:        getenvBool("KUBERPACK_TENANT_ISOLATION", false),
		RequireProjectSecrets:  getenvBool("KUBERPACK_REQUIRE_PROJECT_SECRETS", true),
		PreviewDomain:          getenv("KUBERPACK_PREVIEW_DOMAIN", "preview.host.bzh"),
		InfisicalAuthRef:       getenv("KUBERPACK_INFISICAL_AUTH_REF", "contabo-vps"),
		InfisicalProjectID:     strings.TrimSpace(os.Getenv("KUBERPACK_INFISICAL_PROJECT_ID")),
		InfisicalPath:          getenv("KUBERPACK_INFISICAL_PATH", "/"),
		InfisicalDir:           getenv("KUBERPACK_INFISICAL_DIR", "kubernetes/vps/infisical"),
		InfisicalNamespace:     getenv("KUBERPACK_INFISICAL_NAMESPACE", "infisical"),
		InfisicalOperatorScope: getenvBool("KUBERPACK_INFISICAL_OPERATOR_SCOPE", false),
		PostgresImage:          getenv("KUBERPACK_POSTGRES_IMAGE", ""),
		AddonOperators:         splitList(getenv("KUBERPACK_ADDON_OPERATORS", "")),
		NodeMemory:             getenv("KUBERPACK_NODE_MEMORY", ""),
		NodeCPU:                getenvFloat("KUBERPACK_NODE_CPU", 0),
		// The share of the node the platform keeps for itself. The
		// applications of the platform share what is left.
		NodeReserve:            getenv("KUBERPACK_NODE_RESERVE", ""),
		PostgresStorage:        getenv("KUBERPACK_POSTGRES_STORAGE", ""),
		PostgresStorageClass:   getenv("KUBERPACK_POSTGRES_STORAGE_CLASS", ""),
		AddonStorageClass:      getenv("KUBERPACK_STORAGE_CLASS", ""),
		PreviewDatabaseStorage: getenv("KUBERPACK_PREVIEW_DB_STORAGE", ""),
		PreviewDatabaseClass:   getenv("KUBERPACK_PREVIEW_DB_CLASS", ""),
		ProjectsDir:            getenv("KUBERPACK_PROJECTS_DIR", "kubernetes/vps/projects"),
		ControlPlaneNamespace:  getenv("KUBERPACK_CONTROL_PLANE_NAMESPACE", "kuberpack"),
		BuildNamespace:         getenv("KUBERPACK_BUILDER_NAMESPACE", "kuberpack-build"),
	}
}

func ingressPodSelectorFromEnv() map[string]string {
	var selector map[string]string
	_ = json.Unmarshal([]byte(os.Getenv("KUBERPACK_INGRESS_POD_SELECTOR")), &selector)
	return selector
}

// configuredEgressHosts rend les hôtes des URL configurées, sous la forme
// attendue par l'allowlist.
func configuredEgressHosts(raws ...string) string {
	var hosts []string
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
			hosts = append(hosts, parsed.Host)
			continue
		}
		hosts = append(hosts, raw)
	}
	return strings.Join(hosts, ",")
}

// getenvInt reads an integer from the environment.
func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	value, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return value
}

// getenvFloat reads a number from the environment.
func getenvFloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return value
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

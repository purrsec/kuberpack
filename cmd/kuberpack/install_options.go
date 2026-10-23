package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	"golang.org/x/term"
)

type installCredentials struct{ Git, Registry, API, BuilderGit string }

func runInstall(args []string) error {
	c := setup.Config{Cluster: "default", Namespace: "kuberpack", BuildNamespace: "kuberpack-build", GitOpsBranch: "main", Source: "kuberpack-gitops", Ingress: true, TLS: true, Issuer: "kuberpack-letsencrypt", ClusterServices: true, Infisical: true, Image: "ghcr.io/purrsec/kuberpack:latest", BuilderImage: "ghcr.io/purrsec/kuberpack-builder:latest", Timeout: "15m"}
	creds := installCredentials{Git: os.Getenv("KUBERPACK_GIT_TOKEN"), Registry: os.Getenv("KUBERPACK_REGISTRY_TOKEN"), BuilderGit: os.Getenv("KUBERPACK_BUILDER_GIT_TOKEN")}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	file := fs.String("file", "", "non-secret platform configuration (YAML)")
	contextName := fs.String("context", "", "Kubernetes context (defaults to current context)")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path")
	interactive := fs.Bool("interactive", len(args) == 0 && term.IsTerminal(int(os.Stdin.Fd())), "ask for installation choices")
	dryRun := fs.Bool("dry-run", false, "inspect the cluster and print the plan without writes")
	fs.StringVar(&c.Cluster, "cluster", c.Cluster, "installation name; scopes its GitOps directory")
	fs.StringVar(&c.Namespace, "namespace", c.Namespace, "control plane namespace")
	fs.StringVar(&c.BuildNamespace, "build-namespace", c.BuildNamespace, "builder namespace")
	fs.StringVar(&c.GitProvider, "git-provider", "", "forgejo, github or gitlab (inferred for public providers)")
	fs.StringVar(&c.GitURL, "git-url", "", "forge base URL")
	fs.StringVar(&c.GitUser, "git-user", "", "forge user")
	fs.StringVar(&c.GitOpsURL, "gitops-url", "", "existing GitOps repository URL")
	fs.StringVar(&c.GitOpsBranch, "gitops-branch", c.GitOpsBranch, "GitOps branch")
	fs.StringVar(&c.Root, "gitops-root", "", "dedicated directory (default clusters/<cluster>/kuberpack)")
	fs.StringVar(&c.Source, "gitops-source", c.Source, "Flux GitRepository name")
	fs.StringVar(&creds.Git, "git-token", creds.Git, "forge token (prefer KUBERPACK_GIT_TOKEN)")
	fs.StringVar(&creds.BuilderGit, "builder-git-token", creds.BuilderGit, "dedicated read-only source token (KUBERPACK_BUILDER_GIT_TOKEN); omitted for public sources")
	fs.StringVar(&creds.Registry, "registry-token", creds.Registry, "registry token (prefer KUBERPACK_REGISTRY_TOKEN)")
	fs.StringVar(&creds.API, "api-token", "", "initial API token; existing token is preserved")
	fs.StringVar(&c.RegistryURL, "registry-url", "", "image registry")
	fs.StringVar(&c.RegistryUser, "registry-user", "", "image registry namespace")
	fs.StringVar(&c.Host, "host", "", "public API hostname")
	fs.BoolVar(&c.Ingress, "ingress", true, "expose the API through an existing ingress controller")
	fs.StringVar(&c.IngressClass, "ingress-class", "", "existing IngressClass (auto-detected when unambiguous)")
	fs.StringVar(&c.IngressNamespace, "ingress-namespace", "", "namespace of the ingress controller (auto-detected for Traefik and ingress-nginx)")
	fs.BoolVar(&c.TLS, "tls", true, "request certificates through cert-manager")
	fs.StringVar(&c.Issuer, "issuer", c.Issuer, "existing ClusterIssuer, or a new Kuberpack issuer with --acme-email")
	fs.StringVar(&c.ACMEEmail, "acme-email", "", "ACME registration email for a new issuer")
	fs.StringVar(&c.StorageClass, "storage-class", "", "existing StorageClass (defaults to the cluster default)")
	fs.StringVar(&c.Image, "image", c.Image, "control plane image")
	fs.StringVar(&c.BuilderImage, "builder-image", c.BuilderImage, "builder image")
	fs.StringVar(&c.BuilderMemory, "builder-memory", "3Gi", "memory limit of the builder and vulnerability scan")
	fs.StringVar(&c.BuildkitMemory, "buildkit-memory", "4Gi", "memory limit of BuildKit")
	fs.StringVar(&c.BuildkitImage, "buildkit-image", "", "BuildKit image override")
	fs.IntVar(&c.NodePort, "node-port", 0, "optional NodePort for the API")
	fs.StringVar(&c.WebhookURL, "webhook-url", "", "webhook URL reachable by the forge")
	fs.BoolVar(&c.PrivateBuildEgress, "allow-private-build-egress", false, "allow builds to reach a private forge/registry")
	fs.BoolVar(&c.ClusterServices, "cluster-services", true, "install missing cert-manager and PostgreSQL operators")
	fs.BoolVar(&c.Infisical, "infisical", true, "install and configure Infisical")
	fs.StringVar(&c.InfisicalHost, "infisical-domain", "", "Infisical hostname")
	fs.StringVar(&c.AdminEmail, "infisical-email", "", "initial Infisical administrator email")
	fs.StringVar(&c.CAFile, "ca-file", "", "additional PEM trust bundle for Git and the registry")
	fs.StringVar(&c.Timeout, "timeout", c.Timeout, "timeout per installation stage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected installation arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *file != "" {
		if err := readInstallConfig(*file, &c); err != nil {
			return err
		}
		// Explicit flags override the configuration file, irrespective of order.
		if err := fs.Parse(args); err != nil {
			return err
		}
	}
	if *interactive {
		reader := bufio.NewReader(os.Stdin)
		prompt := func(label string, dest *string) error {
			fmt.Printf("%s [%s]: ", label, *dest)
			line, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			if value := strings.TrimSpace(line); value != "" {
				*dest = value
			}
			return nil
		}
		for _, p := range []struct {
			label string
			value *string
		}{{"Cluster name", &c.Cluster}, {"GitOps repository", &c.GitOpsURL}, {"API hostname", &c.Host}, {"ACME email", &c.ACMEEmail}, {"Infisical administrator email", &c.AdminEmail}} {
			if err := prompt(p.label, p.value); err != nil {
				return err
			}
		}
		for _, p := range []struct {
			label string
			value *string
		}{{"Forge token", &creds.Git}, {"Registry token", &creds.Registry}} {
			if *p.value != "" {
				continue
			}
			fmt.Print(p.label + ": ")
			value, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			*p.value = strings.TrimSpace(string(value))
		}
	}
	if c.Root == "" {
		c.Root = "clusters/" + c.Cluster + "/kuberpack"
	}
	if c.GitURL == "" {
		c.GitURL = forgeBaseURL(c.GitOpsURL)
	}
	if c.GitUser == "" {
		c.GitUser = forgeUser(c.GitOpsURL)
	}
	if c.GitProvider == "" {
		c.GitProvider = "forgejo"
		if strings.Contains(c.GitURL, "github.com") {
			c.GitProvider = "github"
		}
		if strings.Contains(c.GitURL, "gitlab.com") {
			c.GitProvider = "gitlab"
		}
	}
	registry, user := registryOf(c.GitURL, c.GitUser)
	if c.RegistryURL == "" {
		c.RegistryURL = registry
	}
	if c.RegistryUser == "" {
		c.RegistryUser = user
	}
	if c.InfisicalHost == "" {
		c.InfisicalHost = "infisical." + strings.TrimPrefix(c.Host, "kuberpack.")
	}
	if c.AdminEmail == "" {
		c.AdminEmail = "admin@" + strings.TrimPrefix(c.Host, "kuberpack.")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("invalid timeout %q", c.Timeout)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return executeInstallation(ctx, c, creds, *kubeconfig, *contextName, *dryRun, timeout)
}

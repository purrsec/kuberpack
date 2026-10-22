package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runInstall prepares a cluster to run Kuberpack: the secrets, the builder
// and the Flux resources that watch the GitOps repository.
//
// The control plane itself is deployed by the chart, and Flux is installed by
// Flux. This command fills the gap between the two: what only Kuberpack
// knows about.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	namespace := fs.String("namespace", "kuberpack", "namespace of the control plane")
	buildNamespace := fs.String("build-namespace", "kuberpack-build", "namespace of the builder")
	gitUser := fs.String("git-user", "", "user of the GitOps repository (a public repository needs none)")
	gitURL := fs.String("git-url", "", "base URL of the git forge")
	gitopsURL := fs.String("gitops-url", "", "URL of the GitOps repository")
	gitopsBranch := fs.String("gitops-branch", "main", "branch of the GitOps repository")
	gitopsPath := fs.String("gitops-path", "./kubernetes/vps/projects", "path of the projects in the GitOps repository")
	gitopsSource := fs.String("gitops-source", "gitops", "name of the Flux GitRepository")
	gitToken := fs.String("git-token", os.Getenv("KUBERPACK_GIT_TOKEN"), "token that reads the repositories")
	apiToken := fs.String("api-token", "", "token of the API of Kuberpack (the platform generates one, and says it once)")
	registryURL := fs.String("registry-url", "", "OCI registry the images are pushed to")
	registryUser := fs.String("registry-user", "", "namespace of the images in the registry")
	registryToken := fs.String("registry-token", os.Getenv("KUBERPACK_REGISTRY_TOKEN"), "token that pushes the images")
	clusterServices := fs.Bool("cluster-services", true, "install the services of the cluster the platform needs (cert-manager, the operator of the databases)")
	infisical := fs.Bool("infisical", true, "install Infisical and its operator, where the secrets of the applications live")
	infisicalDomain := fs.String("infisical-domain", "", "domain of Infisical (default: infisical.<the domain of the platform>)")
	infisicalEmail := fs.String("infisical-email", "admin@kuberpack.local", "email of the administrator of Infisical")
	dryRun := fs.Bool("dry-run", false, "say what would be done, and do nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*gitToken) == "" {
		return fmt.Errorf("--git-token is required")
	}
	if strings.TrimSpace(*gitopsURL) == "" {
		return fmt.Errorf("--gitops-url is required")
	}
	// What the URL of the GitOps repository says about the forge is taken
	// as the truth: the repository of the user is on the forge of its URL.
	// The user only has to give the URL of the repository and the two
	// tokens.
	if strings.TrimSpace(*gitURL) == "" {
		*gitURL = forgeBaseURL(*gitopsURL)
	}
	if strings.TrimSpace(*gitUser) == "" {
		*gitUser = forgeUser(*gitopsURL)
	}
	if strings.TrimSpace(*registryURL) == "" || strings.TrimSpace(*registryUser) == "" {
		registry, user := registryOf(*gitURL, *gitUser)
		if strings.TrimSpace(*registryURL) == "" {
			*registryURL = registry
		}
		if strings.TrimSpace(*registryUser) == "" {
			*registryUser = user
		}
	}
	// The API of the platform has its own token: the token of the git is
	// for the git. The platform generates it, and says it once.
	generatedToken := false
	if strings.TrimSpace(*apiToken) == "" {
		*apiToken = "kp_" + randomSecret(24)
		generatedToken = true
	}

	// The services of the cluster are installed first: they are what the
	// platform needs, and they do not need the platform.
	if *clusterServices {
		if err := installClusterServices(context.Background(), *dryRun); err != nil {
			return err
		}
	}
	if *dryRun {
		fmt.Printf("kuberpack would be prepared in the namespace %s\n", *namespace)
		fmt.Printf("  forge      %s\n", *gitURL)
		fmt.Printf("  gitops     %s\n", *gitopsURL)
		fmt.Printf("  registry   %s (%s)\n", *registryURL, *registryUser)
		fmt.Printf("  services   %s\n", clusterServicesSummary())
		if *infisical {
			fmt.Printf("  secrets    %s\n", installInfisicalSummary())
		}
		return nil
	}

	config, err := kubeConfig()
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// The namespaces.
	for _, name := range []string{*namespace, *buildNamespace} {
		if err := ensureNamespace(ctx, client, name); err != nil {
			return err
		}
	}
	// The secrets of the control plane.
	if err := ensureSecret(ctx, client, *namespace, "kuberpack", map[string][]byte{
		"KUBERPACK_GIT_TOKEN": []byte(*gitToken),
		"KUBERPACK_API_TOKEN": []byte(*apiToken),
	}); err != nil {
		return err
	}
	// The secrets of the builder: the token that reads the code, and the
	// token that pushes the image. They are not the same.
	if err := ensureSecret(ctx, client, *buildNamespace, "kuberpack-builder", map[string][]byte{
		"git-token":      []byte(*gitToken),
		"registry-token": []byte(*registryToken),
	}); err != nil {
		return err
	}
	// The pull secret of the registry: the builder pulls the base images of
	// the build and the image of the application.
	if strings.TrimSpace(*registryToken) != "" && strings.TrimSpace(*registryUser) != "" {
		host := *registryURL
		if host == "" {
			host = *gitURL
		}
		if err := ensureSecret(ctx, client, *buildNamespace, "kuberpack-builder-pull", map[string][]byte{
			".dockerconfigjson": []byte(dockerConfigJSON(host, *registryUser, *registryToken)),
		}); err != nil {
			return err
		}
	}

	// The credentials of the clone of the GitOps repository. A public
	// repository needs none.
	if strings.TrimSpace(*gitUser) != "" {
		if err := ensureSecret(ctx, client, *namespace, "kuberpack-gitops", map[string][]byte{
			"username": []byte(*gitUser),
			"password": []byte(*gitToken),
		}); err != nil {
			return err
		}
	}
	// Infisical : l'instance, l'operateur, et tout ce qui les relie.
	// L'operateur ne cree aucun YAML : c'est la commande qui ecrit.
	if *infisical {
		domain := strings.TrimSpace(*infisicalDomain)
		if domain == "" {
			domain = "infisical." + platformDomain()
		}
		if err := installInfisical(ctx, config, client, domain, *infisicalEmail, false); err != nil {
			return fmt.Errorf("Infisical: %w", err)
		}
	}
	// The Flux resources that watch the GitOps repository.
	if err := ensureFluxSource(ctx, config, *namespace, *gitopsSource, *gitopsURL, *gitopsBranch); err != nil {
		return fmt.Errorf("the Flux source: %w (is Flux installed? %s)", err, fluxInstallHint)
	}
	if err := ensureFluxProjects(ctx, config, *namespace, *gitopsSource, *gitopsPath); err != nil {
		return fmt.Errorf("the Flux projects: %w (is Flux installed? %s)", err, fluxInstallHint)
	}

	fmt.Printf("kuberpack is prepared in the namespace %s\n", *namespace)
	fmt.Printf("  forge      %s\n", *gitURL)
	fmt.Printf("  gitops     %s\n", *gitopsURL)
	fmt.Printf("  registry   %s (%s)\n", *registryURL, *registryUser)
	if generatedToken {
		fmt.Printf("  api token  %s\n", *apiToken)
		fmt.Println("  this token is shown once: keep it for the CLI (KUBERPACK_API_TOKEN)")
	}
	fmt.Println("the control plane itself is deployed by the chart: helm install kuberpack ./charts/kuberpack --namespace " + *namespace)
	return nil
}

// forgeBaseURL returns the URL of the forge that hosts the repository. The
// repository of the user lives on the forge of its URL.
func forgeBaseURL(repositoryURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host
}

// forgeUser returns the user of the repository: the first segment of the
// path.
func forgeUser(repositoryURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return ""
	}
	return segments[0]
}

// registryOf returns the OCI registry the images are pushed to, and the
// namespace of the images in it. GitHub keeps them in ghcr.io; a self
// hosted forge keeps them where it is.
func registryOf(forgeURL, user string) (registryURL, registryUser string) {
	if strings.TrimSpace(forgeURL) == "" {
		return "", ""
	}
	if strings.Contains(forgeURL, "github.com") {
		return "ghcr.io", user
	}
	return forgeURL, user
}

const fluxInstallHint = "kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml"

// kubeConfig reads the kubeconfig of the machine, or the service account
// when the command runs inside the cluster.
func kubeConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err == nil {
		return config, nil
	}
	clusterConfig, clusterErr := rest.InClusterConfig()
	if clusterErr != nil {
		return nil, fmt.Errorf("no Kubernetes cluster: %w", err)
	}
	return clusterConfig, nil
}

// ensureNamespace creates a namespace when it does not exist.
func ensureNamespace(ctx context.Context, client kubernetes.Interface, name string) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// ensureSecret creates a secret, or replaces its content.
//
// The type of a Secret is immutable. A Secret that is already there keeps
// the type it has: a pull secret is a docker config, and Kubernetes refuses
// the change of type. The type of a Secret that is created is deduced from
// its data, for the same reason.
func ensureSecret(ctx context.Context, client kubernetes.Interface, namespace, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       secretTypeFor(data),
		Data:       data,
	}
	_, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	secret.Type = existing.Type
	secret.ResourceVersion = existing.ResourceVersion
	_, err = client.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

// secretTypeFor returns the type a Secret with this data must have.
func secretTypeFor(data map[string][]byte) corev1.SecretType {
	if _, ok := data[corev1.DockerConfigJsonKey]; ok {
		return corev1.SecretTypeDockerConfigJson
	}
	return corev1.SecretTypeOpaque
}

// dockerConfigJSON is the content of a pull secret.
func dockerConfigJSON(host, user, token string) string {
	auth := base64Encode(user + ":" + token)
	return fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q,"auth":%q}}}`,
		host, user, token, auth)
}

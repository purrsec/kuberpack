package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// The coordinates of the Infisical installation in the cluster.
const (
	infisicalNamespace = "infisical"
	infisicalChartRepo = "https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/"
	// The name of the InfisicalAuth object is the one the platform
	// expects: it is written in the InfisicalStaticSecret of every
	// application that declares secrets.
	infisicalAuthName = "contabo-vps"
	// infisicalInternalAddress is where the secrets operator talks to
	// Infisical: the service of the chart, inside the cluster. The public
	// address is only for the browser.
	infisicalInternalAddress = "http://infisical-infisical-standalone-infisical." + infisicalNamespace + ".svc.cluster.local:8080"
)

// infisicalService is one of the charts of the Infisical installation.
type infisicalService struct {
	// Name is the name of the Helm release.
	Name string
	// Chart is the name of the chart.
	Chart string
	// Version is the version of the chart. It is pinned: a chart that
	// moves is a cluster that changes under the operator's feet.
	Version string
	// Namespace is the namespace of the release.
	Namespace string
	// Values are the values of the chart.
	Values map[string]any
}

// infisicalServices are the charts of the Infisical installation.
//
// The instance and the operator: the operator is what synchronises the
// secrets of the applications. The instance holds them.
func infisicalServices(domain, adminEmail, adminPassword string) []infisicalService {
	return []infisicalService{
		{
			Name:      "infisical",
			Chart:     "infisical-standalone",
			Version:   "1.11.1",
			Namespace: infisicalNamespace,
			Values: map[string]any{
				"infisical": map[string]any{
					"replicaCount": 1,
					"image": map[string]any{
						"repository": "infisical/infisical",
						"tag":        "v0.167.0",
						"pullPolicy": "IfNotPresent",
					},
					"resources": map[string]any{
						"limits":   map[string]any{"memory": "1500Mi"},
						"requests": map[string]any{"cpu": "200m", "memory": "512Mi"},
					},
					// The chart amorce l'instance et depose le jeton
					// de l'identite machine dans un Secret.
					"autoBootstrap": map[string]any{
						"enabled":      true,
						"organization": "kuberpack",
						"secretTemplate": `{"data":{"token":"{{.Identity.Credentials.Token}}",` +
							`"identityId":"{{.Identity.ID}}","organizationId":"{{.Organization.ID}}",` +
							`"email":"{{.User.Email}}"}}`,
						"secretDestination": map[string]any{
							"name":      "infisical-bootstrap-secret",
							"namespace": "",
						},
						"credentialSecret": map[string]any{
							"name": "infisical-bootstrap-credentials",
						},
					},
				},
				"ingress": map[string]any{
					"enabled":          true,
					"hostName":         domain,
					"ingressClassName": "traefik",
					"nginx":            map[string]any{"enabled": false},
					"tls": []any{
						map[string]any{
							"secretName": "infisical-tls",
							"hosts":      []any{domain},
						},
					},
				},
				"postgresql": map[string]any{
					"enabled": true,
					"image": map[string]any{
						"registry":   "mirror.gcr.io",
						"repository": "bitnamilegacy/postgresql",
					},
					"auth": map[string]any{
						"username": "infisical",
						"password": "root",
						"database": "infisicalDB",
					},
					"primary": map[string]any{
						"resources": map[string]any{
							"limits":   map[string]any{"cpu": "500m", "memory": "768Mi"},
							"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
						},
						"persistence": map[string]any{"size": "5Gi"},
					},
				},
				"redis": map[string]any{
					"enabled": true,
					"image": map[string]any{
						"registry":   "mirror.gcr.io",
						"repository": "bitnamilegacy/redis",
					},
					"auth": map[string]any{
						"usePassword": true,
						"password":    "mysecretpassword",
					},
					"master": map[string]any{
						"resources": map[string]any{
							"limits":   map[string]any{"cpu": "300m", "memory": "384Mi"},
							"requests": map[string]any{"cpu": "50m", "memory": "128Mi"},
						},
					},
				},
			},
		},
		{
			// L'operateur : c'est lui qui synchronise les secrets des
			// applications vers les Secrets du cluster.
			Name:      "infisical-operator",
			Chart:     "secrets-operator",
			Version:   "0.11.13",
			Namespace: infisicalNamespace,
			Values:    map[string]any{},
		},
	}
}

// installInfisical installs Infisical and its operator, and wires the
// operator to the instance.
//
// The operator is what the platform uses: it authenticates with a machine
// identity, and that identity is created by the chart when it boots the
// instance. Everything is created here, in one command: the operator
// writes no YAML.
func installInfisical(ctx context.Context, config *rest.Config, client kubernetes.Interface, domain, adminEmail string, dryRun bool) error {
	if domain == "" {
		return fmt.Errorf("the domain of Infisical is required")
	}
	if dryRun {
		for _, service := range infisicalServices(domain, adminEmail, "") {
			fmt.Printf("would install %s (%s %s) in the namespace %s\n",
				service.Name, service.Chart, service.Version, service.Namespace)
		}
		fmt.Printf("would create the service accounts, the roles and the objects of the operator in the namespace %s\n", infisicalNamespace)
		return nil
	}
	if err := ensureNamespace(ctx, client, infisicalNamespace); err != nil {
		return fmt.Errorf("the namespace: %w", err)
	}
	// Les secrets de l'instance : la cle d'authentification et la cle de
	// chiffrement. Elles ne quittent jamais le cluster, et l'operateur les
	// lit. La cle de chiffrement est ecrite a part, sur le disque de
	// l'operateur : sans elle, les secrets de la base sont illisibles.
	// La cle de chiffrement n'est jamais changee : elle chiffre ce qui est
	// deja dans la base. Regeneree, l'instance ne peut plus lire ses
	// propres secrets. Elle n'est donc ecrite que si elle n'existe pas.
	existing, err := client.CoreV1().Secrets(infisicalNamespace).Get(ctx, "infisical-secrets", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	secrets := map[string][]byte{"SITE_URL": []byte("https://" + domain)}
	if err == nil {
		if key := strings.TrimSpace(string(existing.Data["ENCRYPTION_KEY"])); key != "" {
			secrets["ENCRYPTION_KEY"] = existing.Data["ENCRYPTION_KEY"]
		}
		if auth := strings.TrimSpace(string(existing.Data["AUTH_SECRET"])); auth != "" {
			secrets["AUTH_SECRET"] = existing.Data["AUTH_SECRET"]
		}
	}
	if _, ok := secrets["ENCRYPTION_KEY"]; !ok {
		secrets["ENCRYPTION_KEY"] = []byte(randomHex(16))
	}
	if _, ok := secrets["AUTH_SECRET"]; !ok {
		secrets["AUTH_SECRET"] = []byte(randomSecret(32))
	}
	if err := ensureSecret(ctx, client, infisicalNamespace, "infisical-secrets", secrets); err != nil {
		return err
	}
	adminPassword := randomSecret(18)
	if err := ensureSecret(ctx, client, infisicalNamespace, "infisical-bootstrap-credentials", map[string][]byte{
		"INFISICAL_ADMIN_EMAIL":    []byte(adminEmail),
		"INFISICAL_ADMIN_PASSWORD": []byte(adminPassword),
	}); err != nil {
		return err
	}
	// Le certificat : l'instance est servie en HTTPS.
	if err := ensureCertificate(ctx, config, client, domain); err != nil {
		return fmt.Errorf("the certificate: %w", err)
	}
	settings := helmSettings()
	for _, service := range infisicalServices(domain, adminEmail, adminPassword) {
		if err := installInfisicalChart(ctx, settings, config, service); err != nil {
			return fmt.Errorf("the chart %s: %w", service.Name, err)
		}
		fmt.Printf("the chart %s is installed (version %s)\n", service.Name, service.Version)
	}
	// Le chart amorce l'instance et depose le jeton de l'identite machine
	// dans un Secret. L'operateur s'authentifie avec cette identite.
	identityID, err := waitForBootstrapIdentity(ctx, client)
	if err != nil {
		return err
	}
	if err := ensureInfisicalObjects(ctx, config, client, domain, identityID); err != nil {
		return fmt.Errorf("the objects of the operator: %w", err)
	}
	fmt.Printf("Infisical is installed in the namespace %s, on https://%s\n", infisicalNamespace, domain)
	return nil
}

// installInfisicalChart installs or upgrades one chart of the
// installation.
func installInfisicalChart(ctx context.Context, settings *cli.EnvSettings, config *rest.Config, service infisicalService) error {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), service.Namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return err
	}
	loaded, err := fetchInfisicalChart(settings, service)
	if err != nil {
		return err
	}
	history, err := actionConfig.Releases.History(service.Name)
	if err != nil {
		return err
	}
	if len(history) == 0 {
		install := action.NewInstall(actionConfig)
		install.ReleaseName = service.Name
		install.Namespace = service.Namespace
		install.CreateNamespace = true
		install.Wait = true
		install.Timeout = 10 * time.Minute
		_, err = install.RunWithContext(ctx, loaded, service.Values)
		return err
	}
	upgrade := action.NewUpgrade(actionConfig)
	upgrade.Namespace = service.Namespace
	upgrade.Wait = true
	upgrade.Timeout = 10 * time.Minute
	_, err = upgrade.RunWithContext(ctx, service.Name, loaded, service.Values)
	return err
}

// fetchInfisicalChart downloads a chart of the installation and loads it.
func fetchInfisicalChart(settings *cli.EnvSettings, service infisicalService) (*chart.Chart, error) {
	loaded, err := fetchChartFromRepo(settings, infisicalChartRepo, service.Chart, service.Version)
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// waitForBootstrapIdentity waits for the chart to have booted the
// instance, and returns the identifier of the machine identity.
func waitForBootstrapIdentity(ctx context.Context, client kubernetes.Interface) (string, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		secret, err := client.CoreV1().Secrets(infisicalNamespace).Get(ctx, "infisical-bootstrap-secret", metav1.GetOptions{})
		if err == nil {
			if id := strings.TrimSpace(string(secret.Data["identityId"])); id != "" {
				return id, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return "", fmt.Errorf("the instance was not booted in time: the chart did not leave the identity of the machine in the secret infisical-bootstrap-secret")
}

// ensureInfisicalObjects creates the objects the operator needs: the
// service accounts, the role, the connection and the authentication.
func ensureInfisicalObjects(ctx context.Context, config *rest.Config, client kubernetes.Interface, domain, identityID string) error {
	// Le service account que l'operateur presente, et celui dont le jeton
	// permet a Infisical d'interroger TokenReview.
	for _, name := range []string{"infisical-service-account", "infisical-token-reviewer"} {
		account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: infisicalNamespace}}
		if _, err := client.CoreV1().ServiceAccounts(infisicalNamespace).Create(ctx, account, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	// La delegation d'authentification : sans elle, Infisical ne peut pas
	// interroger TokenReview, et l'operateur ne peut pas se presenter.
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "infisical-token-reviewer-binding"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "infisical-token-reviewer", Namespace: infisicalNamespace},
		},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Le secret qui porte l'identite de la machine. L'InfisicalAuth le
	// lit : l'identite n'est jamais ecrit en clair dans un manifeste.
	if err := ensureSecret(ctx, client, infisicalNamespace, "kubernetes-credentials", map[string][]byte{
		"identityId": []byte(identityID),
	}); err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	// La connexion : ou est l'instance.
	//
	// L'operateur parle a Infisical par le service du chart, dans le
	// cluster. L'adresse publique est celle du navigateur : si l'operateur
	// passait par elle, un incident de DNS ou de certificat arreterait la
	// synchronisation de tous les secrets du cluster.
	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "secrets.infisical.com/v1beta1",
		"kind":       "InfisicalConnection",
		"metadata":   map[string]any{"name": "infisical-connection", "namespace": infisicalNamespace},
		"spec":       map[string]any{"address": infisicalInternalAddress},
	}}
	if err := applyUnstructured(ctx, dyn, config, connection); err != nil {
		return err
	}
	// L'authentification : comment l'operateur se presente. Le nom de
	// l'objet est celui que la plateforme attend.
	auth := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "secrets.infisical.com/v1beta1",
		"kind":       "InfisicalAuth",
		"metadata":   map[string]any{"name": infisicalAuthName, "namespace": infisicalNamespace},
		"spec": map[string]any{
			"infisicalConnectionRef": map[string]any{
				"name":      "infisical-connection",
				"namespace": infisicalNamespace,
			},
			"method": "kubernetes",
			"kubernetes": map[string]any{
				"identityIdRef": map[string]any{
					"name":      "kubernetes-credentials",
					"namespace": infisicalNamespace,
					"key":       "identityId",
				},
				"serviceAccountRef": map[string]any{
					"name":      "infisical-service-account",
					"namespace": infisicalNamespace,
				},
			},
		},
	}}
	return applyUnstructured(ctx, dyn, config, auth)
}

// restmapperFor returns the mapper of the cluster, which knows the
// resource of a kind.
func restmapperFor(config *rest.Config) (meta.RESTMapper, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, err
	}
	return restmapper.NewDiscoveryRESTMapper(groupResources), nil
}

// applyUnstructured creates or updates an object the platform does not own
// a typed client for.
func applyUnstructured(ctx context.Context, dyn dynamic.Interface, config *rest.Config, object *unstructured.Unstructured) error {
	// The resource is found by the mapper: the plural of a kind is not
	// the kind in lower case, and guessing it fails.
	group, version := "", ""
	if apiVersion, ok := object.Object["apiVersion"].(string); ok {
		parts := strings.SplitN(apiVersion, "/", 2)
		if len(parts) == 2 {
			group, version = parts[0], parts[1]
		} else {
			version = parts[0]
		}
	}
	mapper, err := restmapperFor(config)
	if err != nil {
		return err
	}
	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: group, Kind: object.GetKind()}, version)
	if err != nil {
		return fmt.Errorf("the resource of %s: %w", object.GetKind(), err)
	}
	resource := dyn.Resource(mapping.Resource)
	_, err = resource.Namespace(object.GetNamespace()).Create(ctx, object, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// The object is already there: it is updated. The version of the
	// existing object is required, the API refuses an update without it.
	existing, err := resource.Namespace(object.GetNamespace()).Get(ctx, object.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	object.SetResourceVersion(existing.GetResourceVersion())
	_, err = resource.Namespace(object.GetNamespace()).Update(ctx, object, metav1.UpdateOptions{})
	return err
}

// randomSecret returns a random secret.
func randomSecret(size int) string {
	return randomHex(size)
}

// randomHex returns a random hexadecimal string.
func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "kuberpack"
	}
	const digits = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0f]
	}
	return string(out)
}

// ensureCertificate asks cert-manager for the certificate of the
// instance. The cluster already has an issuer: the one that signs the
// certificates of the applications.
func ensureCertificate(ctx context.Context, config *rest.Config, client kubernetes.Interface, domain string) error {
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	certificate := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]any{"name": "infisical-tls", "namespace": infisicalNamespace},
		"spec": map[string]any{
			"secretName": "infisical-tls",
			"issuerRef":  map[string]any{"name": "letsencrypt", "kind": "ClusterIssuer"},
			"dnsNames":   []any{domain},
		},
	}}
	return applyUnstructured(ctx, dyn, config, certificate)
}

// helmSettings returns the settings of the SDK Helm.
//
// The SDK reads the cluster on its own: it must be told which
// configuration to use, or it looks for another cluster than the one the
// command talks to.
func helmSettings() *cli.EnvSettings {
	settings := cli.New()
	if path := strings.TrimSpace(os.Getenv("KUBECONFIG")); path != "" {
		settings.KubeConfig = path
	} else if home, err := os.UserHomeDir(); err == nil {
		settings.KubeConfig = filepath.Join(home, ".kube", "config")
	}
	return settings
}

// platformDomain returns the domain the platform is served on, deduced
// from the URL of its API. Infisical lives beside it.
func platformDomain() string {
	url := apiBase()
	trimmed := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if index := strings.Index(trimmed, "/"); index > 0 {
		trimmed = trimmed[:index]
	}
	if trimmed == "" {
		return "kuberpack.local"
	}
	return trimmed
}

// installInfisicalSummary describes the installation, for the output of
// the install command.
func installInfisicalSummary() string {
	return "Infisical (instance, operateur, identite machine), namespace " + infisicalNamespace
}

// fetchChartFromRepo downloads a chart of a repository and loads it.
func fetchChartFromRepo(settings *cli.EnvSettings, repoURL, chartName, version string) (*chart.Chart, error) {
	chartURL, err := repo.FindChartInRepoURL(repoURL, chartName, version, "", "", "", getter.All(settings))
	if err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", chartName, version, err)
	}
	dir, err := os.MkdirTemp("", "kuberpack-chart-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "chart")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	downloader := &downloader.ChartDownloader{
		Out:     os.Stderr,
		Verify:  downloader.VerifyNever,
		Getters: getter.All(settings),
		Options: []getter.Option{},
	}
	saved, _, err := downloader.DownloadTo(chartURL, version, dest)
	if err != nil {
		return nil, fmt.Errorf("the chart %s %s: %w", chartName, version, err)
	}
	if err := untar(saved, dest); err != nil {
		return nil, err
	}
	root := dest
	if _, err := os.Stat(filepath.Join(dest, "Chart.yaml")); err != nil {
		entries, err := os.ReadDir(dest)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dest, entry.Name(), "Chart.yaml")); err == nil {
				root = filepath.Join(dest, entry.Name())
				break
			}
		}
	}
	return loader.Load(root)
}

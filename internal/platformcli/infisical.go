package platformcli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	"k8s.io/apimachinery/pkg/types"
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
	infisicalAuthName = "kuberpack"
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
func infisicalServices(domain string) []infisicalService {
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

						"database": "infisicalDB",
					},
					"primary": map[string]any{
						"automountServiceAccountToken": false,
						"networkPolicy":                infisicalDatabaseNetworkPolicy(),
						"resources": map[string]any{
							"limits":   map[string]any{"cpu": "500m", "memory": "768Mi"},
							"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
						},
						"persistence": map[string]any{"size": "5Gi"},
					},
				},
				"redis": map[string]any{
					"enabled":       true,
					"networkPolicy": infisicalDatabaseNetworkPolicy(),
					"replica":       map[string]any{"automountServiceAccountToken": false},
					"image": map[string]any{
						"registry":   "mirror.gcr.io",
						"repository": "bitnamilegacy/redis",
					},
					"auth": map[string]any{
						"usePassword": true,
					},
					"master": map[string]any{
						"automountServiceAccountToken": false,
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
			Values: map[string]any{
				"scopedRBAC":       true,
				"scopedNamespaces": []string{infisicalNamespace},
			},
		},
	}
}

// fetchInfisicalChart downloads a chart of the installation and loads it.
func fetchInfisicalChart(settings *cli.EnvSettings, service infisicalService) (*chart.Chart, error) {
	loaded, err := fetchChartFromRepo(settings, infisicalChartRepo, service.Chart, service.Version)
	if err != nil {
		return nil, err
	}
	if service.Name == "infisical" {
		if err := hardenInfisicalRuntimeChart(loaded); err != nil {
			return nil, err
		}
	}
	return loaded, nil
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
			{Kind: "ServiceAccount", Name: "infisical-service-account", Namespace: infisicalNamespace},
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
				"serviceAccountTokenAudiences": []any{},
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
	return applyOwnedUnstructured(ctx, dyn, config, object, false)
}

func applyOwnedUnstructured(ctx context.Context, dyn dynamic.Interface, config *rest.Config, object *unstructured.Unstructured, force bool) error {
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
	data, err := json.Marshal(object.Object)
	if err != nil {
		return err
	}
	resource := dyn.Resource(mapping.Resource)
	_, err = resource.Namespace(object.GetNamespace()).Patch(ctx, object.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "kuberpack-install", Force: &force})
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
		panic(fmt.Errorf("secure random generator: %w", err))
	}
	const digits = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0f]
	}
	return string(out)
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

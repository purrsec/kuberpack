package platformcli

import (
	"context"
	"fmt"
	"strings"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	sigsYaml "sigs.k8s.io/yaml"
	"time"
)

// persistentSecret generates absent keys only. It never replaces a password or
// encryption key on reinstall, including after an interrupted Helm bootstrap.
func persistentSecret(ctx context.Context, client interface {
	Get(context.Context, string, metav1.GetOptions) (*corev1.Secret, error)
	Create(context.Context, *corev1.Secret, metav1.CreateOptions) (*corev1.Secret, error)
	Update(context.Context, *corev1.Secret, metav1.UpdateOptions) (*corev1.Secret, error)
}, name string, generate func() map[string][]byte) (*corev1.Secret, error) {
	existing, err := client.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	secret, err := client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: generate()}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return client.Get(ctx, name, metav1.GetOptions{})
	}
	return secret, err
}

func (i *installation) ensureSecretsPlatform(ctx context.Context) error {
	if !i.c.Infisical {
		return nil
	}
	if err := ensureNamespace(ctx, i.kube, infisicalNamespace); err != nil {
		return err
	}
	operatorScope, err := i.seedInfisicalOperatorScope(ctx)
	if err != nil {
		return err
	}
	client := i.kube.CoreV1().Secrets(infisicalNamespace)
	scheme := "http"
	if i.c.TLS {
		scheme = "https"
	}
	_, err = persistentSecret(ctx, client, "infisical-secrets", func() map[string][]byte {
		return map[string][]byte{"SITE_URL": []byte(scheme + "://" + i.c.InfisicalHost), "ENCRYPTION_KEY": []byte(randomHex(16)), "AUTH_SECRET": []byte(randomSecret(32))}
	})
	if err != nil {
		return err
	}
	_, err = persistentSecret(ctx, client, "infisical-bootstrap-credentials", func() map[string][]byte {
		return map[string][]byte{"INFISICAL_ADMIN_EMAIL": []byte(i.c.AdminEmail), "INFISICAL_ADMIN_PASSWORD": []byte(randomSecret(24))}
	})
	if err != nil {
		return err
	}
	sensitive, err := persistentSecret(ctx, client, "infisical-database-credentials", func() map[string][]byte {
		return map[string][]byte{"postgres": []byte(randomSecret(24)), "redis": []byte(randomSecret(24))}
	})
	if err != nil {
		return err
	}
	privateValues := map[string]any{"postgresql": map[string]any{"auth": map[string]any{"password": string(sensitive.Data["postgres"])}}, "redis": map[string]any{"auth": map[string]any{"password": string(sensitive.Data["redis"])}}}
	// Flux reads valuesFrom in the HelmRelease namespace, not targetNamespace.
	data, err := yaml.Marshal(privateValues)
	if err != nil {
		return err
	}
	if err := ensureSecret(ctx, i.kube, i.c.Namespace, "infisical-helm-values", map[string][]byte{"values.yaml": data}); err != nil {
		return err
	}
	for _, inf := range infisicalServices(i.c.InfisicalHost) {
		service := clusterService{Name: inf.Name, Repo: infisicalChartRepo, Chart: inf.Chart, Version: inf.Version, Namespace: inf.Namespace, Values: inf.Values}
		if service.Name == "infisical-operator" {
			service.Values = mergeInstallValues(service.Values, operatorScope)
		}
		if service.Name == "infisical" {
			service.Values["infisical"].(map[string]any)["extraEnv"] = []any{map[string]any{"name": "ALLOW_INTERNAL_IP_CONNECTIONS", "value": "true"}}
			postgres := service.Values["postgresql"].(map[string]any)
			delete(postgres["auth"].(map[string]any), "password")
			postgres["primary"].(map[string]any)["persistence"].(map[string]any)["storageClass"] = i.c.StorageClass
			redis := service.Values["redis"].(map[string]any)
			delete(redis["auth"].(map[string]any), "password")
			redis["master"].(map[string]any)["persistence"] = map[string]any{"storageClass": i.c.StorageClass, "size": "1Gi"}
			ingress := service.Values["ingress"].(map[string]any)
			ingress["ingressClassName"] = i.c.IngressClass
			if !i.c.TLS {
				ingress["tls"] = []any{}
			} else {
				ingress["annotations"] = map[string]any{"cert-manager.io/cluster-issuer": i.c.Issuer}
			}
			service.Values["global"] = map[string]any{"security": map[string]any{"allowInsecureImages": true}}
		}
		chart, err := fetchInfisicalChart(i.settings, inf)
		if err != nil {
			return err
		}
		secrets := map[string]any(nil)
		if service.Name == "infisical" {
			secrets = privateValues
		}
		if err := i.ensureHelm(ctx, service, chart, secrets); err != nil {
			return fmt.Errorf("%s: %w", service.Name, err)
		}
	}
	if err := i.resumeInfisicalBootstrap(ctx); err != nil {
		return err
	}
	var bootstrap *corev1.Secret
	err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		s, err := client.Get(ctx, "infisical-bootstrap-secret", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		bootstrap = s
		return len(s.Data["token"]) > 0 && len(s.Data["organizationId"]) > 0, nil
	})
	if err != nil {
		return fmt.Errorf("Infisical bootstrap credentials: %w", err)
	}
	base, closeForward, err := i.forwardService(ctx, infisicalNamespace, "infisical-infisical-standalone-infisical", 8080)
	if err != nil {
		return err
	}
	defer closeForward()
	api := setup.InfisicalAPI{URL: base, Token: string(bootstrap.Data["token"])}
	identity, err := api.EnsureIdentity(ctx, string(bootstrap.Data["organizationId"]), "kuberpack-"+i.c.Cluster+"-operator")
	if err != nil {
		return err
	}
	// Client JWT TokenReview avoids a stored, expiring reviewer token. Its
	// narrowly bound service account has the auth-delegator role.
	ca, err := i.kube.CoreV1().ConfigMaps(infisicalNamespace).Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return err
	}
	project, err := api.Configure(ctx, "kuberpack-"+i.c.Cluster, identity, string(bootstrap.Data["email"]), ca.Data["ca.crt"])
	if err != nil {
		return err
	}
	i.projectID = project
	if err := ensureInfisicalObjects(ctx, i.config, i.kube, i.c.InfisicalHost, identity); err != nil {
		return err
	}
	if err := i.record(ctx, "infisical.project", project); err != nil {
		return err
	}
	return i.waitReady(ctx, schema.GroupVersionResource{Group: "secrets.infisical.com", Version: "v1beta1", Resource: "infisicalauths"}, infisicalNamespace, infisicalAuthName)
}

// The upstream bootstrap is post-install only. A failed initial installation
// recovered with Helm upgrade would otherwise never run that hook again.
func (i *installation) resumeInfisicalBootstrap(ctx context.Context) error {
	if _, err := i.kube.CoreV1().Secrets(infisicalNamespace).Get(ctx, "infisical-bootstrap-secret", metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	i.settings.SetNamespace(infisicalNamespace)
	configuration := new(action.Configuration)
	if err := configuration.Init(i.settings.RESTClientGetter(), infisicalNamespace, "secret", func(string, ...any) {}); err != nil {
		return err
	}
	rel, err := configuration.Releases.Last("infisical")
	if err != nil {
		return err
	}
	for _, hook := range rel.Hooks {
		if hook.Kind != "Job" || !strings.HasPrefix(hook.Name, "infisical-bootstrap-") {
			continue
		}
		var job batchv1.Job
		// Helm manifests are YAML; Kubernetes JSON field tags require the YAML
		// bridge rather than unmarshalling them with yaml.v3 directly.
		if err := sigsYaml.Unmarshal([]byte(hook.Manifest), &job); err != nil {
			return err
		}
		job.Name = "kuberpack-bootstrap"
		job.Namespace = infisicalNamespace
		job.Annotations = nil
		job.Labels = map[string]string{installOwner: i.c.Cluster}
		_, err = i.kube.BatchV1().Jobs(infisicalNamespace).Create(ctx, &job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return fmt.Errorf("Infisical chart has no bootstrap hook to resume")
}

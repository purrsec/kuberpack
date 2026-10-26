package platformcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	"git.host.bzh/pepe/kuberpack/internal/netcheck"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/cli"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const installOwner = "kuberpack.io/installation"

type installation struct {
	c             setup.Config
	credentials   installCredentials
	settings      *cli.EnvSettings
	config        *rest.Config
	kube          kubernetes.Interface
	dyn           dynamic.Interface
	timeout       time.Duration
	work          string
	appsWork      string
	projectID     string
	fluxNamespace string
	managedFlux   []byte
	managedCharts []clusterService
	controlValues map[string]any
	network       *netcheck.Detection
	ca            []byte
}

func readInstallConfig(file string, c *setup.Config) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(c); err != nil {
		return fmt.Errorf("platform configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("platform configuration must contain one YAML document")
	}
	return nil
}

func executeInstallation(ctx context.Context, c setup.Config, credentials installCredentials, kubeconfig, contextName string, dryRun bool, timeout time.Duration) error {
	if err := validateInstallCredentials(credentials); err != nil {
		return err
	}
	settings := helmSettings()
	if kubeconfig != "" {
		settings.KubeConfig = kubeconfig
	}
	if contextName != "" {
		settings.KubeContext = contextName
	}
	config, err := settings.RESTClientGetter().ToRESTConfig()
	if err != nil {
		return err
	}
	config.Timeout = 30 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	i := &installation{c: c, credentials: credentials, settings: settings, config: config, kube: kube, dyn: dyn, timeout: timeout}
	if err := i.preflight(ctx); err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("Cluster API: %s\nGitOps scope: %s\nStorageClass: %s\nIngressClass: %s\n", config.Host, c.Root, i.c.StorageClass, i.c.IngressClass)
		fmt.Println("Plan: credentials → Flux → missing dependencies → Infisical bootstrap and access → GitOps platform → readiness and authenticated API check")
		fmt.Println("Existing shared controllers and Helmfile releases are reused; no cluster-wide prune or resource adoption.")
		i.printNetworkSummary()
		return nil
	}
	if credentials.Git == "" {
		return fmt.Errorf("env KUBERPACK_GIT_TOKEN is required (read/write GitOps and repository webhooks)")
	}
	if credentials.Registry == "" {
		return fmt.Errorf("env KUBERPACK_REGISTRY_TOKEN is required for the builder")
	}
	if err := validateInstallCredentials(credentials); err != nil {
		return err
	}
	ctx, unlock, err := i.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	parent, err := os.MkdirTemp("", "kuberpack-install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	i.work = filepath.Join(parent, "repo")
	stages := []setup.Step{
		{Name: "Inspect GitOps ownership and prepare the workspace", Run: i.prepareRepository},
		{Name: "Preserve credentials and prepare namespaces", Run: i.prepareCredentials},
		{Name: "Install or reuse Flux", Run: i.ensureFlux},
		{Name: "Install or reuse cluster dependencies", Run: i.ensureDependencies},
		{Name: "Bootstrap Infisical and configure developer access", Run: i.ensureSecretsPlatform},
		{Name: "Publish the platform and hand reconciliation to Flux", Run: i.publishPlatform},
		{Name: "Verify releases and the authenticated API", Run: i.verify},
	}
	for n := range stages {
		run := stages[n].Run
		stages[n].Run = func(ctx context.Context) error {
			bounded, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return run(bounded)
		}
	}
	if err := setup.Run(ctx, os.Stdout, stages); err != nil {
		return err
	}
	fmt.Printf("Kuberpack installed. GitOps scope: %s\n", i.c.Root)
	i.printNetworkSummary()
	if i.c.Ingress {
		scheme := "http"
		if i.c.TLS {
			scheme = "https"
		}
		fmt.Printf("KUBERPACK_API_URL=%s://%s\n", scheme, i.c.Host)
		profile, err := saveInstallProfile(i.c.Cluster, scheme+"://"+i.c.Host, i.credentials.API)
		if err != nil {
			return err
		}
		fmt.Printf("Administrator bootstrap credentials saved privately: %s\nFor kuberpack-admin only: . %s\n", profile, profile)
	}
	fmt.Printf("API credential: Kubernetes Secret %s/kuberpack, key KUBERPACK_API_TOKEN (preserved on rerun).\n", i.c.Namespace)
	if i.c.Infisical {
		fmt.Printf("Infisical project: %s; administrator credentials: Secret %s/infisical-bootstrap-credentials.\n", i.projectID, infisicalNamespace)
	}
	return nil
}

func (i *installation) preflight(ctx context.Context) error {
	version, err := i.kube.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("kubernetes API: %w", err)
	}
	fmt.Printf("Kubernetes %s at %s\n", version.GitVersion, i.config.Host)
	i.detectNetwork(ctx)
	access, err := i.kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verb: "create"}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if !access.Status.Allowed {
		return fmt.Errorf("installation requires permission to manage cluster-wide controllers and RBAC")
	}
	helmConfig, err := i.helmConfiguration(i.c.Namespace)
	if err != nil {
		return err
	}
	existingRelease, err := helmConfig.Releases.Last("kuberpack")
	if err != nil && !isReleaseMissing(err) {
		return err
	}
	if err == nil && existingRelease.Labels[installOwner] != i.c.Cluster {
		return fmt.Errorf("kuberpack release in namespace %s belongs to another workflow; keep its Helmfile or select another namespace", i.c.Namespace)
	}
	installations, err := i.kube.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{LabelSelector: installOwner})
	if err != nil {
		return err
	}
	for _, existing := range installations.Items {
		if existing.Namespace != i.c.Namespace || existing.Labels[installOwner] != i.c.Cluster {
			return fmt.Errorf("kuberpack is already installed as %s in namespace %s; reuse that installation configuration", existing.Labels[installOwner], existing.Namespace)
		}
	}
	for key, wanted := range map[string]string{"gitops.root": i.c.Root, "gitops.url": i.c.GitOpsURL} {
		current, err := i.recorded(ctx, key)
		if err != nil {
			return err
		}
		if current != "" && current != wanted {
			return fmt.Errorf("existing installation uses %s=%s; changing GitOps ownership requires an explicit migration", key, current)
		}
	}
	classes, err := i.kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, class := range classes.Items {
		if i.c.StorageClass == "" && (class.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || class.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true") {
			i.c.StorageClass = class.Name
		}
	}
	found := false
	for _, class := range classes.Items {
		if class.Name == i.c.StorageClass {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no usable default StorageClass; select an existing one with --storage-class")
	}
	if i.c.Ingress {
		classes, err := i.kube.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		if i.c.IngressClass == "" && len(classes.Items) == 1 {
			i.c.IngressClass = classes.Items[0].Name
		}
		found = false
		for _, class := range classes.Items {
			if class.Name == i.c.IngressClass {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("select an existing ingress controller with --ingress-class; Kuberpack does not replace the cluster ingress")
		}
		if i.c.IngressNamespace == "" {
			selector := ""
			for _, class := range classes.Items {
				if class.Name == i.c.IngressClass {
					switch class.Spec.Controller {
					case "traefik.io/ingress-controller":
						selector = "app.kubernetes.io/name=traefik"
					case "k8s.io/ingress-nginx":
						selector = "app.kubernetes.io/name=ingress-nginx,app.kubernetes.io/component=controller"
					}
				}
			}
			if selector != "" {
				if len(i.c.IngressPodSelector) == 0 {
					i.c.IngressPodSelector, err = labels.ConvertSelectorToLabelsMap(selector)
					if err != nil {
						return err
					}
				}
				pods, err := i.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector})
				if err != nil {
					return err
				}
				for _, pod := range pods.Items {
					if pod.DeletionTimestamp != nil {
						continue
					}
					if i.c.IngressNamespace != "" && i.c.IngressNamespace != pod.Namespace {
						return fmt.Errorf("multiple ingress controller namespaces; select --ingress-namespace")
					}
					i.c.IngressNamespace = pod.Namespace
				}
			}
			if i.c.IngressNamespace == "" {
				return fmt.Errorf("cannot determine the ingress data-plane namespace; select --ingress-namespace and verify that this controller is compatible with application NetworkPolicies")
			}
		}
	}
	if i.c.TLS && i.c.ACMEEmail == "" {
		_, err := i.dyn.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}).Get(ctx, i.c.Issuer, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("clusterIssuer %q is unavailable; choose an existing --issuer or provide --acme-email", i.c.Issuer)
		}
	}
	if i.c.CAFile != "" {
		i.c.CAFile, err = filepath.Abs(i.c.CAFile)
		if err != nil {
			return err
		}
		i.ca, err = os.ReadFile(i.c.CAFile)
		if err != nil {
			return err
		}
	}
	return nil
}

func (i *installation) prepareCredentials(ctx context.Context) error {
	if err := validateInstallCredentials(i.credentials); err != nil {
		return err
	}
	if err := ensureNamespace(ctx, i.kube, i.c.Namespace); err != nil {
		return err
	}
	for key, value := range map[string]string{"gitops.root": i.c.Root, "gitops.url": i.c.GitOpsURL} {
		if err := i.record(ctx, key, value); err != nil {
			return err
		}
	}
	// A fresh namespace can be assigned to our chart. Existing namespaces must
	// already belong to this release; Helmfile's namespaces are never adopted.
	ns, err := i.kube.CoreV1().Namespaces().Get(ctx, i.c.BuildNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = i.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: i.c.BuildNamespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}, Annotations: map[string]string{"meta.helm.sh/release-name": "kuberpack", "meta.helm.sh/release-namespace": i.c.Namespace}}}, metav1.CreateOptions{})
	} else if err == nil && (ns.Annotations["meta.helm.sh/release-name"] != "kuberpack" || ns.Annotations["meta.helm.sh/release-namespace"] != i.c.Namespace) {
		return fmt.Errorf("namespace %s belongs to another owner; choose --build-namespace", i.c.BuildNamespace)
	}
	if err != nil {
		return err
	}
	api, err := i.kube.CoreV1().Secrets(i.c.Namespace).Get(ctx, "kuberpack", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && len(api.Data["KUBERPACK_API_TOKEN"]) > 0 {
		if i.credentials.API != "" && i.credentials.API != string(api.Data["KUBERPACK_API_TOKEN"]) {
			return fmt.Errorf("an API token already exists; rotation must be explicit and is not part of install")
		}
		i.credentials.API = string(api.Data["KUBERPACK_API_TOKEN"])
	} else if i.credentials.API == "" {
		i.credentials.API = "kp_" + randomSecret(24)
	}
	if err := validateInstallCredentials(i.credentials); err != nil {
		return err
	}
	if err := ensureSecret(ctx, i.kube, i.c.Namespace, "kuberpack", map[string][]byte{"KUBERPACK_GIT_TOKEN": []byte(i.credentials.Git), "KUBERPACK_API_TOKEN": []byte(i.credentials.API), "KUBERPACK_REGISTRY_TOKEN": []byte(i.credentials.Registry)}); err != nil {
		return err
	}
	if err := ensureSecret(ctx, i.kube, i.c.BuildNamespace, "kuberpack-builder", map[string][]byte{"git-token": []byte(i.credentials.BuilderGit), "registry-token": []byte(i.credentials.Registry)}); err != nil {
		return err
	}
	registry := strings.TrimPrefix(strings.TrimPrefix(i.c.RegistryURL, "https://"), "http://")
	pullCredentials := map[string][]byte{corev1.DockerConfigJsonKey: []byte(dockerConfigJSON(registry, i.c.RegistryUser, i.credentials.Registry))}
	for _, target := range []struct{ namespace, name string }{{i.c.Namespace, "kuberpack-pull"}, {i.c.BuildNamespace, "kuberpack-builder-pull"}} {
		if err := ensureSecret(ctx, i.kube, target.namespace, target.name, pullCredentials); err != nil {
			return err
		}
	}
	gitSecret := map[string][]byte{"username": []byte(i.c.GitUser), "password": []byte(i.credentials.Git)}
	if len(i.ca) > 0 {
		gitSecret["ca.crt"] = i.ca
	}
	return ensureSecret(ctx, i.kube, i.c.Namespace, "kuberpack-gitops", gitSecret)
}

func (i *installation) apply(ctx context.Context, obj *unstructured.Unstructured) error {
	return applyUnstructured(ctx, i.dyn, i.config, obj)
}

func (i *installation) waitReady(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	last := "not observed"
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		obj, err := i.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		generation, hasGeneration, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
		if hasGeneration && generation < obj.GetGeneration() {
			return false, nil
		}
		conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, raw := range conditions {
			c, _ := raw.(map[string]any)
			if c["type"] == "Ready" || c["type"] == "secrets.infisical.com/IsReady" {
				last = fmt.Sprint(c["message"])
				if observed, ok := c["observedGeneration"].(int64); ok && observed < obj.GetGeneration() {
					return false, nil
				}
				return c["status"] == "True", nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("%s/%s: %s: %w", namespace, name, last, err)
	}
	return nil
}

func object(apiVersion, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta, "spec": spec}}
}

func (i *installation) record(ctx context.Context, key, value string) error {
	cm, err := i.kube.CoreV1().ConfigMaps(i.c.Namespace).Get(ctx, "kuberpack-installation", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kuberpack-installation", Namespace: i.c.Namespace, Labels: map[string]string{installOwner: i.c.Cluster}}, Data: map[string]string{key: value}}
		_, err = i.kube.CoreV1().ConfigMaps(i.c.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Data[key] == value {
		return nil
	}
	cm.Data[key] = value
	_, err = i.kube.CoreV1().ConfigMaps(i.c.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

func (i *installation) recorded(ctx context.Context, key string) (string, error) {
	cm, err := i.kube.CoreV1().ConfigMaps(i.c.Namespace).Get(ctx, "kuberpack-installation", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cm.Data[key], nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

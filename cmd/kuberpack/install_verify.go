package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (i *installation) platformValues(ctx context.Context) (map[string]any, error) {
	env := []any{}
	add := func(name, value string) { env = append(env, map[string]any{"name": name, "value": value}) }
	if !i.c.SplitApps() {
		add("KUBERPACK_SELF_CHART_REF", i.c.ChartPath("kuberpack"))
	}
	add("KUBERPACK_WRITE_CHARTS", strconv.FormatBool(!i.c.SplitApps()))
	add("KUBERPACK_TENANT_ISOLATION", strconv.FormatBool(i.c.SplitApps()))
	add("KUBERPACK_RELOADER", strconv.FormatBool(i.c.SplitApps()))
	add("KUBERPACK_REQUIRE_PROJECT_SECRETS", strconv.FormatBool(i.c.RequireProjectSecrets))
	add("KUBERPACK_APPS_SOURCE", func() string {
		if i.c.SplitApps() {
			return "kuberpack-apps"
		}
		return i.c.Source
	}())
	add("KUBERPACK_INFISICAL_DIR", i.c.ProjectsPath())
	add("KUBERPACK_CONTROL_PLANE_NAMESPACE", i.c.Namespace)
	add("KUBERPACK_INGRESS_CLASS", i.c.IngressClass)
	add("KUBERPACK_INGRESS_NAMESPACE", i.c.IngressNamespace)
	if len(i.c.IngressPodSelector) > 0 {
		selector, err := json.Marshal(i.c.IngressPodSelector)
		if err != nil {
			return nil, err
		}
		add("KUBERPACK_INGRESS_POD_SELECTOR", string(selector))
	}
	add("KUBERPACK_INGRESS_TLS", strconv.FormatBool(i.c.TLS))
	add("KUBERPACK_ISSUER", i.c.Issuer)
	add("KUBERPACK_STORAGE_CLASS", i.c.StorageClass)
	add("KUBERPACK_POSTGRES_STORAGE_CLASS", i.c.StorageClass)
	add("KUBERPACK_PREVIEW_DB_CLASS", i.c.StorageClass)
	add("KUBERPACK_TRAEFIK_ENTRYPOINT", "web")
	if i.c.TLS {
		env[len(env)-1].(map[string]any)["value"] = "websecure"
	}
	add("KUBERPACK_ADDON_OPERATORS", "cnpg,barman,mongo,rustfs,valkey,volume")
	if i.c.BuildkitImage != "" {
		add("KUBERPACK_BUILDKIT_IMAGE", i.c.BuildkitImage)
	}
	if i.projectID != "" {
		add("KUBERPACK_INFISICAL_PROJECT_ID", i.projectID)
		add("KUBERPACK_INFISICAL_AUTH_REF", infisicalAuthName)
		add("KUBERPACK_INFISICAL_NAMESPACE", infisicalNamespace)
		add("KUBERPACK_INFISICAL_OPERATOR_SCOPE", "true")
	}
	nodes, err := i.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var totalMemory, totalCPU int64
	for _, node := range nodes.Items {
		if node.Spec.Unschedulable {
			continue
		}
		totalMemory += node.Status.Allocatable.Memory().Value()
		totalCPU += node.Status.Allocatable.Cpu().MilliValue()
	}
	if totalMemory > 0 {
		add("KUBERPACK_NODE_MEMORY", strconv.FormatInt(totalMemory, 10))
		add("KUBERPACK_NODE_CPU", strconv.FormatFloat(float64(totalCPU)/1000, 'f', 3, 64))
		add("KUBERPACK_NODE_RESERVE", "40")
	}
	webhook := i.c.WebhookURL
	if webhook == "" {
		scheme := "http"
		if i.c.TLS {
			scheme = "https"
		}
		webhook = scheme + "://" + i.c.Host + "/hooks/forgejo"
	}
	buildAdmission := map[string]any{"enabled": true}
	if i.c.BuildkitImage != "" {
		buildAdmission["buildkitImage"] = i.c.BuildkitImage
	}
	gitopsURL, gitopsBranch := i.c.GitOpsURL, i.c.GitOpsBranch
	appsDir := i.c.Root + "/apps"
	if i.c.SplitApps() {
		gitopsURL, gitopsBranch = i.c.AppsURL, i.c.AppsBranch
		appsDir = i.c.ProjectsPath()
	}
	// Le placement des builds est optionnel. Vide, il n'est pas écrit dans
	// les valeurs : le chart garde exactement ses défauts historiques.
	placement := map[string]any{}
	if runtimeClass := strings.TrimSpace(i.c.BuilderRuntimeClass); runtimeClass != "" {
		placement["runtimeClass"] = runtimeClass
	}
	if selector, err := builder.ParseNodeSelector(i.c.BuilderNodeSelector); err != nil {
		return nil, err
	} else if len(selector) > 0 {
		placement["nodeSelector"] = selector
	}
	if tolerations, err := builder.ParseTolerations(i.c.BuilderTolerations); err != nil {
		return nil, err
	} else if len(tolerations) > 0 {
		placement["tolerations"] = tolerations
	}
	values := map[string]any{
		"image": i.c.Image, "builderImage": i.c.BuilderImage, "buildNamespace": i.c.BuildNamespace, "storageClass": i.c.StorageClass,
		"imagePullSecrets": []any{map[string]any{"name": "kuberpack-pull"}},
		"builderResources": map[string]any{"cpu": "2", "memory": i.c.BuilderMemory}, "buildkitResources": map[string]any{"cpu": "2", "memory": i.c.BuildkitMemory},
		"gitProvider": i.c.GitProvider, "gitURL": i.c.GitURL, "registryURL": i.c.RegistryURL, "registryUser": i.c.RegistryUser,
		"gitopsURL": gitopsURL, "gitopsBranch": gitopsBranch, "gitopsApps": appsDir, "gitopsProjects": i.c.ProjectsPath(), "gitopsChart": "./" + i.c.ChartPath("stateless"), "gitopsSource": i.c.Source, "gitopsSourceNamespace": i.c.Namespace,
		"host": i.c.Host, "previewDomain": "preview." + i.c.Host, "webhookURL": webhook,
		"ingress": map[string]any{"enabled": i.c.Ingress, "className": i.c.IngressClass, "tls": i.c.TLS, "issuer": func() string {
			if i.c.TLS {
				return i.c.Issuer
			}
			return ""
		}()},
		"nodePort":           map[string]any{"enabled": i.c.NodePort != 0, "port": i.c.NodePort},
		"buildNetworkPolicy": map[string]any{"enabled": !i.c.PrivateBuildEgress}, "tenantIsolation": i.c.SplitApps(), "reloader": map[string]any{"enabled": i.c.SplitApps()}, "extraEnv": env,
		"buildAdmission": buildAdmission,
	}
	if len(placement) > 0 {
		values["builderPlacement"] = placement
	}
	return values, nil
}

func (i *installation) verify(ctx context.Context) error {
	if err := i.waitControlPlaneValues(ctx); err != nil {
		return err
	}
	for _, item := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}, i.c.Source},
		{schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, "kuberpack-platform"},
		{schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, "kuberpack"},
	} {
		if err := i.waitReady(ctx, item.gvr, i.c.Namespace, item.name); err != nil {
			return err
		}
	}
	if i.c.SplitApps() {
		if err := i.waitReady(ctx, schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}, i.c.Namespace, "kuberpack-apps"); err != nil {
			return err
		}
	} else {
		if err := i.waitReady(ctx, schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, i.c.Namespace, "kuberpack-projects"); err != nil {
			return err
		}
	}
	for _, service := range i.managedCharts {
		if service.Name == "kuberpack" {
			continue
		}
		if err := i.waitReady(ctx, schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, i.c.Namespace, "kuberpack-"+service.Name); err != nil {
			return err
		}
	}
	base, closeForward, err := i.forwardService(ctx, i.c.Namespace, "kuberpack", 8080)
	if err != nil {
		return err
	}
	defer closeForward()
	for _, check := range []struct {
		path, token string
		status      int
	}{{"/healthz", "", 200}, {"/api/v1/apps", "", 401}, {"/api/v1/apps", i.credentials.API, 200}} {
		req, err := http.NewRequestWithContext(ctx, "GET", base+check.path, nil)
		if err != nil {
			return err
		}
		if check.token != "" {
			req.Header.Set("Authorization", "Bearer "+check.token)
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return fmt.Errorf("API verification: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != check.status {
			return fmt.Errorf("API verification %s: HTTP %d, expected %d", check.path, resp.StatusCode, check.status)
		}
	}
	if i.c.Ingress {
		scheme := "http"
		if i.c.TLS {
			scheme = "https"
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			return err
		}
		if len(i.ca) > 0 {
			pool.AppendCertsFromPEM(i.ca)
		}
		client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
		for _, host := range []string{i.c.Host, func() string {
			if i.c.Infisical {
				return i.c.InfisicalHost
			}
			return ""
		}()} {
			if host == "" {
				continue
			}
			path := "/healthz"
			if host == i.c.InfisicalHost {
				path = "/api/status"
			}
			req, err := http.NewRequestWithContext(ctx, "GET", scheme+"://"+host+path, nil)
			if err != nil {
				return err
			}
			var last error
			if err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(context.Context) (bool, error) {
				response, err := client.Do(req)
				if err != nil {
					last = err
					return false, nil
				}
				response.Body.Close()
				if response.StatusCode != 200 {
					last = fmt.Errorf("HTTP %d", response.StatusCode)
					return false, nil
				}
				return true, nil
			}); err != nil {
				return fmt.Errorf("public endpoint %s: %v: %w", host, last, err)
			}
		}
	}
	return i.record(ctx, "status", "ready")
}

// A healthy release can still describe the previous Git revision. Wait for
// Flux to apply the requested values before checking its readiness generation.
func (i *installation) waitControlPlaneValues(ctx context.Context) error {
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		release, err := i.dyn.Resource(gvr).Namespace(i.c.Namespace).Get(ctx, "kuberpack", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		values, found, err := unstructured.NestedMap(release.Object, "spec", "values")
		return found && sameJSON(values, i.controlValues), err
	})
	if err != nil {
		return fmt.Errorf("waiting for Flux to apply the requested control plane configuration: %w", err)
	}
	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func isReleaseMissing(err error) bool { return errors.Is(err, driver.ErrReleaseNotFound) }

func (i *installation) helmConfiguration(namespace string) (*action.Configuration, error) {
	i.settings.SetNamespace(namespace)
	configuration := new(action.Configuration)
	if err := configuration.Init(i.settings.RESTClientGetter(), namespace, "secret", func(string, ...any) {}); err != nil {
		return nil, err
	}
	return configuration, nil
}

// ensureHelm installs missing releases, reuses releases owned by others, and
// leaves upgrades of already reconciled releases to Flux. It never adopts a
// release deployed by Helmfile or another platform.
func (i *installation) ensureHelm(ctx context.Context, s clusterService, loaded *chart.Chart, sensitive map[string]any) error {
	// Helm's storage namespace and Kubernetes resource builder namespace are
	// separate settings. Charts may omit metadata.namespace on resources.
	configuration, err := i.helmConfiguration(s.Namespace)
	if err != nil {
		return err
	}
	current, err := configuration.Releases.Last(s.Name)
	if err != nil && !isReleaseMissing(err) {
		return err
	}
	if err == nil && current.Labels[installOwner] != i.c.Cluster {
		if s.Name == "kuberpack" {
			return fmt.Errorf("Helm release %s/%s is managed externally; keep its Helmfile workflow or choose a separate namespace", s.Namespace, s.Name)
		}
		if current.Chart.Metadata.Name != loaded.Metadata.Name {
			return fmt.Errorf("Helm release %s/%s contains another chart; refusing adoption", s.Namespace, s.Name)
		}
		if current.Info.Status != release.StatusDeployed {
			return fmt.Errorf("external Helm release %s/%s is %s; repair it with its owner", s.Namespace, s.Name, current.Info.Status)
		}
		fmt.Printf("  reuse external Helm release %s/%s; values and version untouched\n", s.Namespace, s.Name)
		return nil
	}
	i.managedCharts = append(i.managedCharts, s)
	values := s.Values
	if sensitive != nil {
		values = mergeInstallValues(s.Values, sensitive)
	}
	if err == nil && current.Info.Status == release.StatusDeployed {
		handedOver, readErr := i.recorded(ctx, "gitops.handed-over")
		if readErr != nil {
			return readErr
		}
		if handedOver == "true" || (sameJSON(current.Config, values) && current.Chart.Metadata.Version == loaded.Metadata.Version) {
			fmt.Printf("  %s/%s already installed\n", s.Namespace, s.Name)
			return nil
		}
	}
	if err != nil {
		install := action.NewInstall(configuration)
		install.ReleaseName = s.Name
		install.Namespace = s.Namespace
		install.CreateNamespace = true
		install.Wait = true
		install.WaitForJobs = true
		install.Timeout = i.timeout
		install.Labels = map[string]string{installOwner: i.c.Cluster}
		_, err = install.RunWithContext(ctx, loaded, values)
		return err
	}
	// A process killed during an install/upgrade can leave Helm's pending lock.
	// The installation Lease ensures the previous installer is no longer active.
	if current.Info.Status.IsPending() {
		current.Info.Status = release.StatusFailed
		current.Info.Description = "Interrupted Kuberpack installation; retrying"
		if err := configuration.Releases.Update(current); err != nil {
			return err
		}
	}
	upgrade := action.NewUpgrade(configuration)
	upgrade.Namespace = s.Namespace
	upgrade.Wait = true
	upgrade.WaitForJobs = true
	upgrade.Timeout = i.timeout
	upgrade.Labels = map[string]string{installOwner: i.c.Cluster}
	_, err = upgrade.RunWithContext(ctx, s.Name, loaded, values)
	return err
}

func mergeInstallValues(base, overlay map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range base {
		result[key] = value
	}
	for key, value := range overlay {
		if child, ok := value.(map[string]any); ok {
			if old, ok := result[key].(map[string]any); ok {
				result[key] = mergeInstallValues(old, child)
				continue
			}
		}
		result[key] = value
	}
	return result
}

func (i *installation) ensureDependencies(ctx context.Context) error {
	if !i.c.ClusterServices {
		return nil
	}
	groups := map[string]schema.GroupVersionResource{
		"cert-manager":        {Group: "cert-manager.io", Version: "v1", Resource: "certificates"},
		"cloudnative-pg":      {Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"},
		"plugin-barman-cloud": {Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"},
	}
	for _, service := range clusterServices() {
		managed, err := i.recorded(ctx, "chart."+service.Name)
		if err != nil {
			return err
		}
		_, err = i.dyn.Resource(groups[service.Name]).List(ctx, metav1.ListOptions{Limit: 1})
		if err == nil && managed == "" {
			fmt.Printf("  reuse existing %s operator\n", service.Name)
			continue
		}
		if managed == "" {
			if err := i.record(ctx, "chart."+service.Name, "managed"); err != nil {
				return err
			}
		}
		loaded, err := fetchChart(i.settings, service)
		if err != nil {
			return err
		}
		if err := i.ensureHelm(ctx, service, loaded, nil); err != nil {
			return fmt.Errorf("%s: %w", service.Name, err)
		}
	}
	if i.c.TLS && i.c.ACMEEmail != "" {
		gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
		existing, err := i.dyn.Resource(gvr).Get(ctx, i.c.Issuer, metav1.GetOptions{})
		if err == nil && existing.GetLabels()[installOwner] != i.c.Cluster {
			return nil
		}
		issuer := object("cert-manager.io/v1", "ClusterIssuer", "", i.c.Issuer, map[string]any{"acme": map[string]any{"email": i.c.ACMEEmail, "server": "https://acme-v02.api.letsencrypt.org/directory", "privateKeySecretRef": map[string]any{"name": i.c.Issuer}, "solvers": []any{map[string]any{"http01": map[string]any{"ingress": map[string]any{"ingressClassName": i.c.IngressClass}}}}}})
		issuer.SetLabels(map[string]string{installOwner: i.c.Cluster})
		if err := i.apply(ctx, issuer); err != nil {
			return err
		}
		if err := i.waitReady(ctx, gvr, "", i.c.Issuer); err != nil {
			return err
		}
	}
	return nil
}

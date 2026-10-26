package platformcli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const fluxManifestURL = "https://github.com/fluxcd/flux2/releases/download/v2.9.6/install.yaml"
const fluxManifestSHA = "9c1fda7e401429531ed1478f67ba06b3edff6513b75da7ee47bde9f1c4d4251c"

func (i *installation) ensureFlux(ctx context.Context) error {
	managed, err := i.recorded(ctx, "flux.namespace")
	if err != nil {
		return err
	}
	deployments, err := i.kube.AppsV1().Deployments("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/part-of=flux"})
	if err != nil {
		return err
	}
	if managed == "" {
		counts := map[string]int{}
		for _, deployment := range deployments.Items {
			if deployment.Name == "source-controller" || deployment.Name == "kustomize-controller" || deployment.Name == "helm-controller" {
				counts[deployment.Namespace]++
			}
		}
		for ns, count := range counts {
			if count == 3 {
				if i.fluxNamespace != "" {
					return fmt.Errorf("multiple Flux installations found; choose an unambiguous cluster context")
				}
				i.fluxNamespace = ns
			}
		}
		if len(counts) > 0 && i.fluxNamespace == "" {
			return fmt.Errorf("an incomplete external Flux installation exists; repair it before installing Kuberpack")
		}
		if i.fluxNamespace == "" {
			if _, err := i.kube.Discovery().ServerResourcesForGroupVersion("kustomize.toolkit.fluxcd.io/v1"); err == nil {
				return fmt.Errorf("flux CRDs exist without the required controllers; repair this external installation before running Kuberpack")
			}
		}
		if i.fluxNamespace != "" {
			for _, deployment := range deployments.Items {
				if deployment.Namespace != i.fluxNamespace || deployment.Name != "helm-controller" {
					continue
				}
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name != "manager" {
						continue
					}
					parts := strings.Split(container.Image, ":")
					v, err := semver.NewVersion(parts[len(parts)-1])
					minimum := semver.MustParse("1.5.0")
					if err != nil || v.LessThan(minimum) {
						return fmt.Errorf("existing helm-controller image %q is incompatible; use Flux >=2.8", container.Image)
					}
				}
			}
			fmt.Printf("  reuse shared Flux in %s\n", i.fluxNamespace)
			return i.waitFlux(ctx)
		}
		managed = "flux-system"
		if err := i.record(ctx, "flux.namespace", managed); err != nil {
			return err
		}
	}
	i.fluxNamespace = managed
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fluxManifestURL, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("flux manifests: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != fluxManifestSHA {
		return fmt.Errorf("flux manifest checksum mismatch")
	}
	i.managedFlux = data
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if obj.GetKind() == "" {
			continue
		}
		// Only the Flux instance recorded as ours is reconciled here. Shared
		// Flux returned above is never patched or upgraded by this installer.
		if err := applyOwnedUnstructured(ctx, i.dyn, i.config, obj, true); err != nil {
			return fmt.Errorf("flux %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return i.waitFlux(ctx)
}

func (i *installation) waitFlux(ctx context.Context) error {
	for _, name := range []string{"source-controller", "kustomize-controller", "helm-controller"} {
		if err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
			d, err := i.kube.AppsV1().Deployments(i.fluxNamespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return d.Status.ObservedGeneration >= d.Generation && d.Status.AvailableReplicas > 0, nil
		}); err != nil {
			return fmt.Errorf("flux %s: %w", name, err)
		}
	}
	return nil
}

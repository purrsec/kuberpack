package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"git.host.bzh/pepe/kuberpack/internal/store"
)

// The values of an application travel from the GitOps repository to the
// cluster in a ConfigMap. The platform writes it, and asks the release to
// reconcile at once.
//
// Without this, the values are read by a controller that runs on its own
// schedule: a change of the manifest is applied in the interval of the
// controller, and the release may be upgraded with the values of the
// previous version of the manifest. The manifest is the truth: it must be
// applied when it changes, not later.

// syncAppValues writes the values of the application in the cluster and
// asks its release to reconcile at once.
func (s *Server) syncAppValues(ctx context.Context, app store.App, namespace string) error {
	if s.cfg.Kube == nil {
		return nil
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil
	}
	values, err := s.gitOpsValues(ctx, app)
	if err != nil {
		return fmt.Errorf("read the values of %s: %w", app.Name, err)
	}
	service := releaseName(app)
	name := service + "-values"
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string]string{"values.yaml": string(values)},
	}
	_, err = s.cfg.Kube.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		// The ConfigMap is created: the release will read it.
	} else if apierrors.IsAlreadyExists(err) {
		patch, err := patchBytes(map[string]any{"data": cm.Data})
		if err != nil {
			return err
		}
		if _, err := s.cfg.Kube.CoreV1().ConfigMaps(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("write the values of %s: %w", app.Name, err)
		}
	} else {
		return fmt.Errorf("write the values of %s: %w", app.Name, err)
	}
	// The release reconciles on its own schedule anyway: asking it to
	// reconcile now is an accelerator, not a condition.
	if err := s.forceRelease(ctx, namespace, service); err != nil {
		log.Printf("kuberpack app %s: the release: %v", app.Name, err)
	}
	return nil
}

// releaseName is the name of the release of an application: the name of
// the service, without the project it belongs to. The identity of an
// application in the database is <project>-<service>, the release of the
// application is named after the service.
func releaseName(app store.App) string {
	name := strings.TrimSpace(app.Name)
	project := strings.TrimSpace(app.Project)
	if project == "" {
		return name
	}
	return strings.TrimPrefix(name, project+"-")
}

// forceRelease asks the helm controller to reconcile the release of an
// application now. The annotation is the one Flux reads to know that a
// reconcile is wanted: the release does not wait for its interval.
func (s *Server) forceRelease(ctx context.Context, namespace, name string) error {
	dyn, err := s.dynamicClient()
	if err != nil {
		return err
	}
	releases := dyn.Resource(schema.GroupVersionResource{
		Group:    "helm.toolkit.fluxcd.io",
		Version:  "v2",
		Resource: "helmreleases",
	}).Namespace(namespace)
	patch, err := patchBytes(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				"reconcile.fluxcd.io/requestedAt": time.Now().UTC().Format(time.RFC3339),
			},
		},
	})
	if err != nil {
		return err
	}
	if _, err := releases.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			// The release does not exist yet: the values of the
			// application are written, and the release will read them.
			return nil
		}
		return fmt.Errorf("ask the release of %s to reconcile: %w", name, err)
	}
	return nil
}

// dynamicClient builds a client for the resources of Flux, which have no
// Go type.
func (s *Server) dynamicClient() (dynamic.Interface, error) {
	if s.cfg.KubeDynamic != nil {
		return s.cfg.KubeDynamic, nil
	}
	if s.cfg.KubeConfig == nil {
		return nil, fmt.Errorf("the cluster is not known")
	}
	return dynamic.NewForConfig(s.cfg.KubeConfig)
}

// patchBytes writes a merge patch, without the type of the object.
func patchBytes(data map[string]any) ([]byte, error) {
	return json.Marshal(data)
}

// Package reloader restarts the workloads that consume a synchronised Secret
// when the value of that Secret changes.
//
// The Infisical operator updates the Secret, but a running pod keeps the value
// it read at startup. The reloader is deliberately a separate component: it
// has its own identity and reads only the Secrets of the projects that grant
// it a RoleBinding. The control plane never gains that read access.
package reloader

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	// AutoReloadAnnotation is written by the compiler on a project service
	// that declares secrets. It opts the Deployment into the reloader.
	AutoReloadAnnotation = "secrets.infisical.com/auto-reload"
	// SecretVersionAnnotation is the etag the Infisical operator writes on
	// the synchronised Secret when its value changes.
	SecretVersionAnnotation = "secrets.infisical.com/version"
	// RevisionAnnotation is written by the reloader on the pod template.
	RevisionAnnotation = "kuberpack.io/secret-revision"
)

// Run reconciles at startup and then at every interval.
func Run(ctx context.Context, client kubernetes.Interface, interval time.Duration, log func(string, ...any)) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		changed, err := Reconcile(ctx, client)
		if err != nil {
			log("reloader: %v", err)
		} else if changed > 0 {
			log("reloader: %d workload(s) restarted", changed)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Reconcile restarts the opted-in Deployments of every project namespace whose
// consumed Secret has a new version. It returns the number of patched workloads.
func Reconcile(ctx context.Context, client kubernetes.Interface) (int, error) {
	namespaces, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: "kuberpack.io/project"})
	if err != nil {
		return 0, fmt.Errorf("list project namespaces: %w", err)
	}
	total := 0
	var errs []error
	for _, namespace := range namespaces.Items {
		deployments, err := client.AppsV1().Deployments(namespace.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("list deployments in %s: %w", namespace.Name, err))
			continue
		}
		for i := range deployments.Items {
			deployment := &deployments.Items[i]
			if deployment.Annotations[AutoReloadAnnotation] != "true" {
				continue
			}
			revision, ok, err := consumedRevision(ctx, client, namespace.Name, deployment)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !ok {
				continue
			}
			if deployment.Spec.Template.Annotations[RevisionAnnotation] == revision {
				continue
			}
			if err := patchRevision(ctx, client, deployment, revision); err != nil {
				errs = append(errs, err)
				continue
			}
			total++
		}
	}
	return total, join(errs)
}

func join(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// consumedRevision returns a stable revision for the Secrets consumed by the
// Deployment. A Secret without the operator version annotation is ignored: no
// value change was observed for it.
func consumedRevision(ctx context.Context, client kubernetes.Interface, namespace string, deployment *appsv1.Deployment) (string, bool, error) {
	names := consumedSecretNames(deployment.Spec.Template.Spec)
	if len(names) == 0 {
		return "", false, nil
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", false, fmt.Errorf("read Secret %s/%s: %w", namespace, name, err)
		}
		version := secret.Annotations[SecretVersionAnnotation]
		if strings.TrimSpace(version) == "" {
			continue
		}
		parts = append(parts, name+"="+version)
	}
	if len(parts) == 0 {
		return "", false, nil
	}
	sort.Strings(parts)
	return strings.Join(parts, ","), true, nil
}

func consumedSecretNames(spec corev1.PodSpec) []string {
	seen := map[string]bool{}
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			seen[name] = true
		}
	}
	for _, container := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, ref := range container.EnvFrom {
			if ref.SecretRef != nil {
				add(ref.SecretRef.Name)
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				add(env.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	for _, volume := range spec.Volumes {
		if volume.Secret != nil {
			add(volume.Secret.SecretName)
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func patchRevision(ctx context.Context, client kubernetes.Interface, deployment *appsv1.Deployment, revision string) error {
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, RevisionAnnotation, revision)
	_, err := client.AppsV1().Deployments(deployment.Namespace).Patch(ctx, deployment.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch Deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
	}
	return nil
}

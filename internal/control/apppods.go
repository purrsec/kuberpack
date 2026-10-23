package control

import (
	"context"
	"fmt"
	"sort"

	"git.host.bzh/pepe/kuberpack/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ownedAppPods resolves active pods through controller UIDs. Matching labels
// alone must never grant access to another workload's logs or metrics.
func (s *Server) ownedAppPods(ctx context.Context, app store.App) (*appsv1.Deployment, []corev1.Pod, error) {
	d, err := s.cfg.Kube.AppsV1().Deployments(app.Namespace).Get(ctx, releaseName(app), metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("application deployment is unavailable (%s)", apierrors.ReasonForError(err))
	}
	if d.Spec.Selector == nil {
		return nil, nil, fmt.Errorf("application deployment has no pod selector")
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("application deployment has an invalid pod selector")
	}
	sets, err := s.cfg.Kube.AppsV1().ReplicaSets(app.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate application replicas (%s)", apierrors.ReasonForError(err))
	}
	owners := make(map[types.UID]bool)
	for _, set := range sets.Items {
		owner := metav1.GetControllerOf(&set)
		if owner != nil && owner.Kind == "Deployment" && owner.UID == d.UID {
			owners[set.UID] = true
		}
	}
	pods, err := s.cfg.Kube.CoreV1().Pods(app.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate application pods (%s)", apierrors.ReasonForError(err))
	}
	var selected []corev1.Pod
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if pod.DeletionTimestamp == nil && owner != nil && owner.Kind == "ReplicaSet" && owners[owner.UID] {
			selected = append(selected, pod)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return d, selected, nil
}

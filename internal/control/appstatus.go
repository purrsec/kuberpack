package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ReplicaStatus struct {
	Desired   int32 `json:"desired"`
	Ready     int32 `json:"ready"`
	Available int32 `json:"available"`
	Updated   int32 `json:"updated"`
}

type ReconciliationStatus struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// DeploymentStatus is observed from the cluster on request, independently of
// build history. Missing observations never imply a healthy deployment.
type DeploymentStatus struct {
	State          string               `json:"state"`
	Reason         string               `json:"reason,omitempty"`
	Message        string               `json:"message"`
	Namespace      string               `json:"namespace,omitempty"`
	Release        string               `json:"release,omitempty"`
	Image          string               `json:"image,omitempty"`
	Replicas       *ReplicaStatus       `json:"replicas,omitempty"`
	Reconciliation ReconciliationStatus `json:"reconciliation"`
	ObservedAt     time.Time            `json:"observed_at"`
	Resources      *AppResources        `json:"resources,omitempty"`
}

func (s *Server) getAppStatus(w http.ResponseWriter, r *http.Request) {
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		app, err = s.cfg.Store.ImageAppByName(r.Context(), r.PathValue("name"))
	}
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status := s.observeApp(ctx, app)
	status.Resources = s.observeAppResources(ctx, app)
	writeJSON(w, http.StatusOK, map[string]any{"name": app.Name, "project": app.Project, "hostname": app.Hostname, "deployment": status})
}

func (s *Server) observedApps(ctx context.Context, apps []store.App) []map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out := make([]map[string]any, len(apps))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(apps)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				out[index] = appJSON(apps[index])
				out[index]["deployment"] = s.observeApp(ctx, apps[index])
			}
		}()
	}
	for index := range apps {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return out
}

func (s *Server) observeApp(ctx context.Context, app store.App) DeploymentStatus {
	out := DeploymentStatus{State: "unknown", Namespace: app.Namespace, Release: releaseName(app), ObservedAt: time.Now().UTC(), Reconciliation: ReconciliationStatus{State: "unknown"}}
	unknown := func(message string) DeploymentStatus {
		out.State = "unknown"
		out.Reason = "ObservationUnavailable"
		out.Message = message
		return out
	}
	if err := ctx.Err(); err != nil {
		return unknown("Deployment observation timed out")
	}
	if s.cfg.Kube == nil {
		return unknown("The control plane cannot observe the Kubernetes cluster")
	}
	if out.Namespace == "" {
		return unknown("Deployment location is not available yet; recompile kuberpack.yaml")
	}
	deployment, err := s.cfg.Kube.AppsV1().Deployments(out.Namespace).Get(ctx, out.Release, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return unknown("Cannot read deployment status: " + string(apierrors.ReasonForError(err)))
	}
	missing := apierrors.IsNotFound(err)
	if !missing {
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		out.Replicas = &ReplicaStatus{Desired: desired, Ready: deployment.Status.ReadyReplicas, Available: deployment.Status.AvailableReplicas, Updated: deployment.Status.UpdatedReplicas}
		if len(deployment.Spec.Template.Spec.Containers) > 0 {
			out.Image = deployment.Spec.Template.Spec.Containers[0].Image
		}
	}
	dyn, err := s.dynamicClient()
	if err != nil {
		return unknown("The control plane cannot observe deployment reconciliation")
	}
	release, err := dyn.Resource(schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}).Namespace(out.Namespace).Get(ctx, out.Release, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		out.Reconciliation = ReconciliationStatus{State: "pending", Reason: "ReleasePending", Message: "Waiting for the deployment to be applied"}
	} else if err != nil {
		return unknown("Cannot read deployment reconciliation: " + string(apierrors.ReasonForError(err)))
	} else {
		out.Reconciliation = releaseStatus(release)
	}
	if missing {
		out.State = "pending"
		out.Reason = "DeploymentPending"
		out.Message = "Waiting for the deployment to be created"
		if out.Reconciliation.State == "failed" {
			out.State = "failed"
			out.Reason = out.Reconciliation.Reason
			out.Message = out.Reconciliation.Message
		}
		return out
	}
	out = deploymentReadiness(out, deployment)
	if out.State == "running" || out.State == "stopped" || out.State == "failed" || out.Reconciliation.State == "failed" || deployment.Status.ObservedGeneration < deployment.Generation {
		return out
	}
	// Inspect only the ReplicaSet for the latest deployment revision. Errors
	// in terminating pods or an old rollout must not describe the new rollout.
	reason, message, err := s.currentPodProblem(ctx, deployment)
	if err != nil {
		return unknown("Cannot read application replicas: " + string(apierrors.ReasonForError(err)))
	}
	if reason != "" && out.Reconciliation.State != "failed" {
		out.State = "failed"
		if out.Replicas.Available > 0 {
			out.State = "degraded"
		}
		if reason == "Unschedulable" && out.Replicas.Available == 0 {
			out.State = "pending"
		}
		out.Reason = reason
		out.Message = message
	}
	return out
}

func releaseStatus(release *unstructured.Unstructured) ReconciliationStatus {
	if suspended, _, _ := unstructured.NestedBool(release.Object, "spec", "suspend"); suspended {
		return ReconciliationStatus{State: "suspended", Reason: "ReconciliationSuspended", Message: "Automatic deployment updates are suspended"}
	}
	out := ReconciliationStatus{State: "reconciling", Reason: "ReconciliationPending", Message: "Waiting for deployment reconciliation"}
	conditions, _, _ := unstructured.NestedSlice(release.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		generation, ok := condition["observedGeneration"].(int64)
		if !ok {
			generation, _, _ = unstructured.NestedInt64(release.Object, "status", "observedGeneration")
		}
		if generation < release.GetGeneration() {
			continue
		}
		kind, _ := condition["type"].(string)
		status, _ := condition["status"].(string)
		reason, _ := condition["reason"].(string)
		message, _ := condition["message"].(string)
		if kind == "Reconciling" && status == "True" {
			return ReconciliationStatus{State: "reconciling", Reason: reason, Message: message}
		}
		if kind == "Stalled" && status == "True" {
			out = ReconciliationStatus{State: "failed", Reason: reason, Message: message}
			continue
		}
		if kind == "Ready" && out.State != "failed" {
			if status == "True" {
				out = ReconciliationStatus{State: "ready", Reason: reason, Message: message}
			} else if status == "False" {
				out = ReconciliationStatus{State: "failed", Reason: reason, Message: message}
			}
		}
	}
	return out
}

func deploymentReadiness(out DeploymentStatus, d *appsv1.Deployment) DeploymentStatus {
	out.State = "deploying"
	out.Reason = "RolloutInProgress"
	out.Message = "Waiting for the requested replicas to become available"
	if d.Status.ObservedGeneration < d.Generation {
		out.Reason = "GenerationPending"
		return out
	}
	if out.Reconciliation.State == "failed" {
		out.State = "failed"
		if out.Replicas.Available > 0 {
			out.State = "degraded"
		}
		out.Reason = out.Reconciliation.Reason
		out.Message = out.Reconciliation.Message
		return out
	}
	for _, condition := range d.Status.Conditions {
		if (condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded") || (condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue) {
			out.State = "failed"
			if out.Replicas.Available > 0 {
				out.State = "degraded"
			}
			out.Reason = condition.Reason
			out.Message = condition.Message
			return out
		}
	}
	if out.Reconciliation.State != "ready" && out.Reconciliation.State != "suspended" {
		out.Reason = out.Reconciliation.Reason
		out.Message = out.Reconciliation.Message
		return out
	}
	if out.Replicas.Desired == 0 && d.Status.Replicas == 0 {
		out.State = "stopped"
		out.Reason = "ScaledToZero"
		out.Message = "The application is intentionally stopped"
		return out
	}
	if d.Spec.Paused {
		out.Reason = "RolloutPaused"
		out.Message = "The deployment rollout is paused"
		return out
	}
	if out.Replicas.Desired > 0 && out.Replicas.Updated == out.Replicas.Desired && out.Replicas.Ready == out.Replicas.Desired && out.Replicas.Available == out.Replicas.Desired && d.Status.Replicas == out.Replicas.Desired {
		out.State = "running"
		out.Reason = "ReplicasReady"
		out.Message = fmt.Sprintf("All %d requested replicas are available", out.Replicas.Desired)
	}
	return out
}

func (s *Server) currentPodProblem(ctx context.Context, d *appsv1.Deployment) (string, string, error) {
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return "", "", err
	}
	sets, err := s.cfg.Kube.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", "", err
	}
	var current *appsv1.ReplicaSet
	var revision int64 = -1
	for index := range sets.Items {
		set := &sets.Items[index]
		owner := metav1.GetControllerOf(set)
		if owner == nil || owner.Kind != "Deployment" || owner.UID != d.UID {
			continue
		}
		number, _ := strconv.ParseInt(set.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		if current == nil || number > revision {
			current = set
			revision = number
		}
	}
	if current == nil {
		return "", "", nil
	}
	pods, err := s.cfg.Kube.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", "", err
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.Kind != "ReplicaSet" || owner.UID != current.UID || pod.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" {
				return condition.Reason, condition.Message, nil
			}
		}
		for _, container := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			if container.Ready {
				continue
			}
			if waiting := container.State.Waiting; waiting != nil {
				switch waiting.Reason {
				case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "InvalidImageName":
					return waiting.Reason, fmt.Sprintf("Container %s cannot start (%s)", container.Name, waiting.Reason), nil
				}
			}
			if stopped := container.State.Terminated; stopped != nil && stopped.ExitCode != 0 {
				return stopped.Reason, fmt.Sprintf("Container %s exited with code %d (%s)", container.Name, stopped.ExitCode, stopped.Reason), nil
			}
		}
	}
	return "", "", nil
}

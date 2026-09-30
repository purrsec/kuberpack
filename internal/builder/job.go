package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"git.host.bzh/pepe/kuberpack/internal/fail"
)

const defaultBuildkitImage = "docker.io/moby/buildkit:v0.33.0-rootless@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef"

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Request struct {
	DeliveryID      string
	CloneURL        string
	CommitSHA       string
	ImageRepository string
	RegistryUser    string
	StartCmd        string
	BuildID         int64
}

type Runner interface {
	Build(context.Context, Request) error
}

type JobRunner struct {
	Client         kubernetes.Interface
	Namespace      string
	Image          string
	BuildkitImage  string
	SecretName     string
	PullSecretName string
	Timeout        time.Duration
	PollInterval   time.Duration
}

func InCluster(image string) (*JobRunner, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("Kubernetes in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &JobRunner{Client: client, Image: image}, nil
}

// Build submits one Job and waits for its terminal status. A caller must only
// look up the OCI digest after this returns successfully: an old tag cannot
// satisfy a build that has not finished.
func (r *JobRunner) Build(ctx context.Context, req Request) error {
	job, err := r.NewJob(req)
	if err != nil {
		return fail.Stage(fail.Job, err)
	}
	created, err := r.Client.BatchV1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) && job.Name != "" {
		created, err = r.Client.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if err == nil && created.Annotations["kuberpack.dev/request"] != job.Annotations["kuberpack.dev/request"] {
			return fail.Text(fail.Job, "existing job belongs to a different request")
		}
	}
	if err != nil {
		return fail.Stage(fail.Job, err)
	}
	fmt.Printf("kuberpack build Job: %s/%s\n", created.Namespace, created.Name)
	interval := r.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	limit := r.Timeout
	if limit <= 0 {
		limit = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		current, err := r.Client.BatchV1().Jobs(created.Namespace).Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return fail.Stage(fail.Job, err)
		}
		if current.Status.Succeeded > 0 {
			return nil
		}
		if current.Status.Failed > 0 || jobCondition(current.Status.Conditions, batchv1.JobFailed) {
			if msg := r.podFailureMessage(ctx, created); msg != "" {
				return fail.Text("", msg)
			}
			return fail.Text(fail.Job, "failed")
		}
		select {
		case <-ctx.Done():
			return fail.Text(fail.Job, "timeout")
		case <-time.After(interval):
		}
	}
}

func jobCondition(conditions []batchv1.JobCondition, kind batchv1.JobConditionType) bool {
	for _, c := range conditions {
		if c.Type == kind && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *JobRunner) podFailureMessage(ctx context.Context, job *batchv1.Job) string {
	pods, err := r.Client.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job.Name})
	if err != nil || len(pods.Items) == 0 {
		pods, err = r.Client.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + job.Name})
	}
	if err != nil {
		return ""
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name != "builder" || cs.State.Terminated == nil {
				continue
			}
			if msg := strings.TrimSpace(cs.State.Terminated.Message); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func (r *JobRunner) NewJob(req Request) (*batchv1.Job, error) {
	if r.Image == "" || r.Client == nil {
		return nil, fmt.Errorf("builder image and Kubernetes client are required")
	}
	if !commitPattern.MatchString(req.CommitSHA) || req.CloneURL == "" || req.ImageRepository == "" || req.RegistryUser == "" {
		return nil, fmt.Errorf("full lowercase commit SHA, clone URL, image repository and registry user are required")
	}
	if !strings.HasPrefix(req.CloneURL, "https://") {
		return nil, fmt.Errorf("builder clone URL must use HTTPS")
	}
	namespace := r.Namespace
	if namespace == "" {
		namespace = "kuberpack-build"
	}
	secret := r.SecretName
	if secret == "" {
		secret = "kuberpack-builder"
	}
	pullSecret := r.PullSecretName
	if pullSecret == "" {
		pullSecret = "kuberpack-builder-pull"
	}
	buildkitImage := r.BuildkitImage
	if buildkitImage == "" {
		buildkitImage = defaultBuildkitImage
	}
	deadline := int64(3600)
	if r.Timeout > 0 && r.Timeout < time.Hour {
		deadline = int64(r.Timeout.Seconds())
		if deadline < 1 {
			deadline = 1
		}
	}
	backoff := int32(0)
	ttl := int32(86400)
	uid := int64(1000)
	allowEscalation, noEscalation := true, false
	readOnly := true
	never := false
	rootlessRestart := corev1.ContainerRestartPolicyAlways
	cacheSize := resource.MustParse("8Gi")
	workSize := resource.MustParse("8Gi")
	tmpSize := resource.MustParse("2Gi")
	requestDigest := sha256.Sum256([]byte(req.CloneURL + "\x00" + req.CommitSHA + "\x00" + req.ImageRepository + "\x00" + req.RegistryUser + "\x00" + req.StartCmd))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    namespace,
			GenerateName: "kuberpack-build-",
			Labels:       map[string]string{"app.kubernetes.io/name": "kuberpack-build", "kuberpack.dev/commit": req.CommitSHA},
			Annotations:  map[string]string{"kuberpack.dev/build-id": fmt.Sprint(req.BuildID), "kuberpack.dev/request": hex.EncodeToString(requestDigest[:])},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "kuberpack-build"}},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					NodeSelector:                 map[string]string{"kubernetes.io/arch": "amd64"},
					ServiceAccountName:           "kuberpack-build",
					AutomountServiceAccountToken: &never,
					ImagePullSecrets:             []corev1.LocalObjectReference{{Name: pullSecret}},
					SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid},
					InitContainers: []corev1.Container{{
						Name: "buildkitd", Image: buildkitImage, ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"rootlesskit", "buildkitd"},
						Args:            []string{"--addr=unix:///run/buildkit/buildkitd.sock", "--oci-worker-no-process-sandbox"},
						RestartPolicy:   &rootlessRestart,
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation, RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "buildkit-socket", MountPath: "/run/buildkit"}, {Name: "buildkit-cache", MountPath: "/home/user/.local/share/buildkit"}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("6Gi")}},
					}},
					Containers: []corev1.Container{{
						Name: "builder", Image: r.Image, ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/usr/bin/kuberpack", "builder"},
						Env:             []corev1.EnvVar{{Name: "KUBERPACK_CLONE_URL", Value: req.CloneURL}, {Name: "KUBERPACK_COMMIT_SHA", Value: req.CommitSHA}, {Name: "KUBERPACK_IMAGE_REPOSITORY", Value: req.ImageRepository}, {Name: "KUBERPACK_REGISTRY_USER", Value: req.RegistryUser}, {Name: "KUBERPACK_START_CMD", Value: req.StartCmd}, {Name: "HOME", Value: "/work"}, {Name: "TMPDIR", Value: "/work"}, {Name: "TRIVY_CACHE_DIR", Value: "/work/trivy-cache"}},
						WorkingDir:      "/work",
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noEscalation, RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "buildkit-socket", MountPath: "/run/buildkit"}, {Name: "work", MountPath: "/work"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "tmp", MountPath: "/var/tmp"}, {Name: "secrets", MountPath: "/secrets", ReadOnly: true}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}},
					}},
					Volumes: []corev1.Volume{
						{Name: "buildkit-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "buildkit-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &cacheSize}}},
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &workSize}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpSize}}},
						{Name: "secrets", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret}}},
					},
				},
			},
		},
	}
	if req.DeliveryID != "" {
		id := sha256.Sum256([]byte(req.CloneURL + "\x00" + req.DeliveryID))
		job.Name = "kuberpack-build-" + hex.EncodeToString(id[:8])
		job.GenerateName = ""
	}
	return job, nil
}

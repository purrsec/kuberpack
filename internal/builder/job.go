package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
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

const (
	defaultBuildkitImage = "docker.io/moby/buildkit:v0.33.0-rootless@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef"
	terminationWait      = 20 * time.Second
)

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Request struct {
	DeliveryID      string
	CloneURL        string
	CommitSHA       string
	ImageRepository string
	RegistryUser    string
	// GenerateLock tells the builder to resolve the Python dependencies when
	// the repository has no lock file.
	GenerateLock bool
	// GitAuthScheme is how the forge of the application authenticates a
	// clone: "token" or "basic". It is not a secret.
	GitAuthScheme string
	StartCmd      string
	RootDirectory string
	BuildCommand  string
	BuildID       int64
	BuildsGitURL  string
	AppName       string
	Environment   string
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
	BuildsGitURL   string
	Timeout        time.Duration
	PollInterval   time.Duration
	// The resources of the builder and of its BuildKit sidecar.
	BuilderCPU     string
	BuilderMemory  string
	BuildkitCPU    string
	BuildkitMemory string
}

// resourceQuantity reads a quantity, with a default.
func resourceQuantity(value, fallback string) resource.Quantity {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return resource.MustParse(fallback)
	}
	return quantity
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
		if err == nil {
			if created.Annotations["kuberpack.dev/request"] != job.Annotations["kuberpack.dev/request"] {
				return fail.Text(fail.Job, "existing job belongs to a different request")
			}
			// The Job name is stable per delivery, so a retry of the same
			// commit finds the previous Job. A failed one must not block the
			// retry until its TTL expires: delete it and start a new one.
			// A running or succeeded Job is kept, so a control-plane restart
			// never triggers a second build.
			if jobFailed(created) {
				background := metav1.DeletePropagationBackground
				_ = r.Client.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &background})
				created, err = r.Client.BatchV1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{})
				if err != nil {
					return fail.Stage(fail.Job, err)
				}
			}
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
	var failedAt time.Time
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
			// kubelet often marks the Job failed before Terminated.Message is visible.
			if failedAt.IsZero() {
				failedAt = time.Now()
			}
			if time.Since(failedAt) >= terminationWait {
				return fail.Text(fail.Job, "failed")
			}
		}
		select {
		case <-ctx.Done():
			if !failedAt.IsZero() {
				return fail.Text(fail.Job, "failed")
			}
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

// jobFailed reports whether a Job already reached a terminal failure.
func jobFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	return job.Status.Failed > 0 || jobCondition(job.Status.Conditions, batchv1.JobFailed)
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
	if !commitPattern.MatchString(req.CommitSHA) || req.ImageRepository == "" || req.RegistryUser == "" {
		return nil, fmt.Errorf("full lowercase commit SHA, image repository and registry user are required")
	}
	// The builder clones over HTTPS using the token mounted at
	// /secrets/git-token. Kuberpack does not manage an SSH deploy key.
	cloneURL := strings.TrimSpace(req.CloneURL)
	if cloneURL == "" {
		return nil, fmt.Errorf("clone URL is required")
	}
	if !strings.HasPrefix(cloneURL, "https://") {
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
	// Keep finished Jobs long enough to inspect a failure, but not long enough
	// to exhaust the builder namespace pod quota (see charts/kuberpack quota).
	ttl := int32(3600)
	uid := int64(1000)
	allowEscalation, noEscalation := true, false
	readOnly := true
	never := false
	rootlessRestart := corev1.ContainerRestartPolicyAlways
	cacheSize := resource.MustParse("8Gi")
	workSize := resource.MustParse("8Gi")
	tmpSize := resource.MustParse("2Gi")
	// Trivy downloads and unpacks a vulnerability database of a few hundred
	// megabytes: the builder container needs more than 2Gi to survive the scan.
	// The resources of the build are the size of the machine that builds.
	// They are configurable: a small server builds small applications.
	builderCPU := resourceQuantity(r.BuilderCPU, "4")
	builderMemory := resourceQuantity(r.BuilderMemory, "6Gi")
	buildkitCPU := resourceQuantity(r.BuildkitCPU, "6")
	buildkitMemory := resourceQuantity(r.BuildkitMemory, "8Gi")
	requestDigest := sha256.Sum256([]byte(cloneURL + "\x00" + req.CommitSHA + "\x00" + req.ImageRepository + "\x00" + req.RegistryUser + "\x00" + req.StartCmd))
	buildsURL := strings.TrimSpace(req.BuildsGitURL)
	if buildsURL == "" {
		buildsURL = strings.TrimSpace(r.BuildsGitURL)
	}
	env := []corev1.EnvVar{
		{Name: "KUBERPACK_CLONE_URL", Value: cloneURL},
		{Name: "KUBERPACK_COMMIT_SHA", Value: req.CommitSHA},
		{Name: "KUBERPACK_IMAGE_REPOSITORY", Value: req.ImageRepository},
		{Name: "KUBERPACK_REGISTRY_USER", Value: req.RegistryUser},
		{Name: "KUBERPACK_GENERATE_LOCK", Value: strconv.FormatBool(req.GenerateLock)},
		{Name: "KUBERPACK_GIT_AUTH", Value: req.GitAuthScheme},
		{Name: "KUBERPACK_START_CMD", Value: req.StartCmd},
		{Name: "KUBERPACK_ROOT_DIRECTORY", Value: req.RootDirectory},
		{Name: "KUBERPACK_BUILD_CMD", Value: req.BuildCommand},
		{Name: "KUBERPACK_BUILDS_GIT_URL", Value: buildsURL},
		{Name: "KUBERPACK_APP_NAME", Value: req.AppName},
		{Name: "KUBERPACK_ENVIRONMENT", Value: req.Environment},
		{Name: "HOME", Value: "/work"},
		{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
		{Name: "TMPDIR", Value: "/work"},
		{Name: "TRIVY_CACHE_DIR", Value: "/work/trivy-cache"},
	}
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
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: buildkitCPU, corev1.ResourceMemory: buildkitMemory}},
					}},
					Containers: []corev1.Container{{
						Name: "builder", Image: r.Image, ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/usr/bin/kuberpack", "builder"},
						Env:             env,
						WorkingDir:      "/work",
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noEscalation, RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "buildkit-socket", MountPath: "/run/buildkit"}, {Name: "work", MountPath: "/work"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "tmp", MountPath: "/var/tmp"}, {Name: "secrets", MountPath: "/secrets", ReadOnly: true}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: builderCPU, corev1.ResourceMemory: builderMemory}},
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
		id := sha256.Sum256([]byte(cloneURL + "\x00" + req.DeliveryID))
		job.Name = "kuberpack-build-" + hex.EncodeToString(id[:8])
		job.GenerateName = ""
	}
	return job, nil
}

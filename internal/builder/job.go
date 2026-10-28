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
	// ControlTokenDigest is the fingerprint of the token the control
	// plane uses. The builder must not have it: the builder runs the
	// code of the build, and that code must not be able to write in the
	// repositories of the platform.
	ControlTokenDigest string
	// StrictTokens refuses the build when the builder holds the token of
	// the control plane. The default is a warning: the operator decides
	// when the platform is strict.
	StrictTokens    bool
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
	// CloneAnonymous prevents credentials of the platform from being sent to
	// public repositories, including repositories on another forge.
	CloneAnonymous bool
	// BuildsGitAuthScheme is how the forge of the builds repository
	// authenticates a clone. It is not a secret.
	BuildsGitAuthScheme string
	// EgressAllow est l'allowlist d'accès sortants du control plane. Le
	// builder clone le dépôt de l'application : il doit appliquer la même
	// politique, sans quoi une forge interne serait refusée.
	EgressAllow string

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
	// RuntimeClass donne une sandbox au Pod du build (gVisor, Kata).
	// Vide conserve le runtime par défaut : le build partage alors le
	// noyau du cluster, comme avant ce réglage.
	RuntimeClass string
	// NodeSelector place les builds sur des nœuds dédiés. Le builder y
	// ajoute toujours l'architecture par défaut.
	NodeSelector map[string]string
	// Tolerations permet aux builds d'occuper le pool tainté qui les
	// accueille. Une tolérance sans clé est refusée : elle ouvrirait tous
	// les taints du cluster.
	Tolerations []corev1.Toleration
	// AcceptedVulnerabilities are the CVE the platform accepts. A finding
	// of the scan that is not in this list stops the build.
	AcceptedVulnerabilities []string
	// NodeMemory is the memory of the node the builds run on. The bounds
	// of the builder follow it: a build that asks for more than the
	// machine can give does not fail, it takes the node down.
	NodeMemory string
	// NodeDisk is the size of the disk of the node the builds run on. The
	// volumes of a build follow it: a build that writes more than the
	// node can hold evicts itself, and the kubelet evicts the pods that
	// write the most.
	NodeDisk string
	// BuildkitCache is the room of the cache of BuildKit, in megabytes:
	// "Reserved,Free,Maximum". BuildKit deletes the oldest entries of
	// the cache when it grows past the maximum. Without a limit, the
	// cache of a build fills the disk of the node.
	BuildkitCache string
}

// DeliveryLabel is the value of the label that names the delivery of a
// build.
//
// A label holds 63 characters at most. The name of a delivery is longer
// when the delivery is a redeploy: the label holds a fingerprint of it, and
// the platform looks the pod of a build up the same way.
func DeliveryLabel(deliveryID string) string {
	id := strings.TrimSpace(deliveryID)
	if id == "" {
		return ""
	}
	if len(id) <= 63 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "d-" + hex.EncodeToString(sum[:])[:32]
}

// buildkitCacheLimit is the room of the cache of BuildKit.
//
// The cache holds the layers of the dependencies and of the images of the
// base. It is a cache: BuildKit deletes the oldest entries when it grows
// past the maximum, and the build keeps the room it needs.
//
// The value is the one of BuildKit: the space that is reserved, the space
// that must stay free, and the maximum, in megabytes.
func (r *JobRunner) buildkitCacheLimit() string {
	if value := strings.TrimSpace(r.BuildkitCache); value != "" {
		return value
	}
	return "100,500,1000"
}

// memoryLimits returns the memory the builder and BuildKit may use.
//
// The two run together, so they share the node. By default they take
// half of it each, and the other half is left to the platform and to
// the applications.
func (r *JobRunner) memoryLimits() (builder, buildkit string) {
	total := r.NodeMemory
	if strings.TrimSpace(total) == "" {
		return "2Gi", "2Gi"
	}
	quantity, err := resource.ParseQuantity(total)
	if err != nil {
		return "2Gi", "2Gi"
	}
	// The two run together: they take a quarter of the node each, and
	// the other half is left to the platform and to the applications.
	quarter := quantity.Value() / 4
	if quarter < 256*1024*1024 {
		quarter = 256 * 1024 * 1024
	}
	half := quarter
	limit := strconv.FormatInt(half, 10)
	return limit, limit
}

// volumeSizes returns the size of the volumes of a build.
//
// The work holds the source, the cache of BuildKit and the database of
// the vulnerabilities. They are bounded: a build must not fill the
// disk of the node.
//
// The bounds follow the size of the disk of the node: a build on a small
// node gets small volumes, and a build on a big node gets room. The
// limits are a declaration: the kubelet does not always apply them, and
// the builder measures the disk before it starts.
func (r *JobRunner) volumeSizes() (work, cache, tmp resource.Quantity) {
	total, err := resource.ParseQuantity(strings.TrimSpace(r.NodeDisk))
	if err != nil || total.IsZero() {
		return resource.MustParse("3Gi"), resource.MustParse("8Gi"), resource.MustParse("1Gi")
	}
	// The volumes must leave the node room to breathe: the kubelet
	// evicts the pods that write the most when the disk crosses its
	// threshold, and a build is the pod that writes the most.
	workSize := total.Value() * 5 / 100
	cacheSize := total.Value() * 3 / 100
	tmpSize := total.Value() * 1 / 100
	return resource.MustParse(fmt.Sprintf("%d", maxInt64(workSize, 1<<30))),
		// Active build layers cannot be garbage-collected. A normal Node
		// build exceeds 1 GiB even when the retained cache target is smaller.
		resource.MustParse(fmt.Sprintf("%d", maxInt64(cacheSize, 2<<30))),
		resource.MustParse(fmt.Sprintf("%d", maxInt64(tmpSize, 256<<20)))
}

// maxInt64 returns the bigger of the two numbers.
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
		return nil, fmt.Errorf("kubernetes in-cluster config: %w", err)
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
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
		case <-ticker.C:
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
		if pod.Status.Reason != "" && pod.Status.Message != "" {
			return fmt.Sprintf("job: %s: %s", pod.Status.Reason, strings.TrimSpace(pod.Status.Message))
		}
		// BuildKit is a native sidecar: its OOM is recorded with the init
		// containers, while the builder only sees a disconnected buildctl.
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			terminated := cs.State.Terminated
			if terminated == nil || terminated.Reason != "OOMKilled" {
				terminated = cs.LastTerminationState.Terminated
			}
			if terminated == nil || terminated.Reason != "OOMKilled" {
				continue
			}
			limit := ""
			containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
			for _, container := range containers {
				if container.Name == cs.Name {
					if quantity, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
						limit = " (memory limit " + quantity.String() + ")"
					}
				}
			}
			return fmt.Sprintf("job: %s OOMKilled%s: increase the platform build memory limit", cs.Name, limit)
		}
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
	// Le placement est validé ici aussi : un appelant qui construit un
	// JobRunner directement ne doit pas pouvoir placer un build sur un
	// nœud arbitraire avec un RuntimeClass inconnu.
	runtimeClass := strings.TrimSpace(r.RuntimeClass)
	if err := ValidateRuntimeClass(runtimeClass); err != nil {
		return nil, err
	}
	nodeSelector, err := NormalizeNodeSelector(r.NodeSelector)
	if err != nil {
		return nil, err
	}
	tolerations, err := NormalizeTolerations(r.Tolerations)
	if err != nil {
		return nil, err
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
	// to exhaust the builder namespace pod quota (see provisioning/charts/kuberpack quota).
	ttl := int32(3600)
	uid := int64(1000)
	allowEscalation, noEscalation := true, false
	readOnly := true
	never := false
	rootlessRestart := corev1.ContainerRestartPolicyAlways
	// The bounds of the builder follow the node. A build that asks for
	// more than the machine can give does not fail: it takes the node
	// down, and the platform that shares it falls with it.
	workSize, cacheSize, tmpSize := r.volumeSizes()
	builderMemoryLimit, buildkitMemoryLimit := r.memoryLimits()
	// Trivy downloads and unpacks a vulnerability database of a few hundred
	// megabytes: the builder container needs more than 2Gi to survive the scan.
	// The resources of the build are the size of the machine that builds.
	// They are configurable: a small server builds small applications.
	builderCPU := resourceQuantity(r.BuilderCPU, "2")
	builderMemory := resourceQuantity(r.BuilderMemory, builderMemoryLimit)
	buildkitCPU := resourceQuantity(r.BuildkitCPU, "2")
	buildkitMemory := resourceQuantity(r.BuildkitMemory, buildkitMemoryLimit)
	requestDigest := sha256.Sum256([]byte(cloneURL + "\x00" + req.CommitSHA + "\x00" + req.ImageRepository + "\x00" + req.RegistryUser + "\x00" + req.StartCmd + "\x00" + strconv.FormatBool(req.CloneAnonymous)))
	buildsURL := strings.TrimSpace(req.BuildsGitURL)
	if buildsURL == "" {
		buildsURL = strings.TrimSpace(r.BuildsGitURL)
	}
	env := []corev1.EnvVar{
		{Name: "KUBERPACK_CLONE_URL", Value: cloneURL},
		// The fingerprint of the token of the control plane. It is not a
		// secret: a fingerprint cannot be reversed. It tells the builder
		// that it was handed the wrong token.
		{Name: "KUBERPACK_CONTROL_FINGERPRINT", Value: req.ControlTokenDigest},
		{Name: "KUBERPACK_COMMIT_SHA", Value: req.CommitSHA},
		{Name: "KUBERPACK_IMAGE_REPOSITORY", Value: req.ImageRepository},
		{Name: "KUBERPACK_REGISTRY_USER", Value: req.RegistryUser},
		{Name: "KUBERPACK_STRICT_CREDENTIALS", Value: strconv.FormatBool(req.StrictTokens)},
		{Name: "KUBERPACK_GENERATE_LOCK", Value: strconv.FormatBool(req.GenerateLock)},
		{Name: "KUBERPACK_GIT_AUTH", Value: req.GitAuthScheme},
		{Name: "KUBERPACK_GIT_ANONYMOUS", Value: strconv.FormatBool(req.CloneAnonymous)},
		{Name: "KUBERPACK_BUILDS_GIT_AUTH", Value: req.BuildsGitAuthScheme},
		{Name: "KUBERPACK_TRIVY_IGNORE", Value: strings.Join(r.AcceptedVulnerabilities, ",")},
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
		// The space the build writes. The builder refuses to start
		// when the node cannot hold it. Active BuildKit layers must count:
		// garbage collection cannot reclaim them during the build.
		{Name: "KUBERPACK_BUILD_DISK_NEED", Value: strconv.FormatInt(workSize.Value()+cacheSize.Value()+tmpSize.Value(), 10)},
	}
	if allow := strings.TrimSpace(req.EgressAllow); allow != "" {
		env = append(env, corev1.EnvVar{Name: "KUBERPACK_EGRESS_ALLOW", Value: allow})
	}
	var runtimeClassName *string
	if runtimeClass != "" {
		runtimeClassName = &runtimeClass
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    namespace,
			GenerateName: "kuberpack-build-",
			Labels: map[string]string{
				"app.kubernetes.io/name": "kuberpack-build",
				"kuberpack.dev/commit":   req.CommitSHA,
				// The delivery names the build. The platform finds the
				// pod of a build by it, to read its logs.
				"kuberpack.dev/delivery": DeliveryLabel(req.DeliveryID),
			},
			Annotations: map[string]string{"kuberpack.dev/build-id": fmt.Sprint(req.BuildID), "kuberpack.dev/request": hex.EncodeToString(requestDigest[:])},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					"app.kubernetes.io/name": "kuberpack-build",
					// The pod carries the delivery and the commit: the
					// platform finds the pod of a build by them, to
					// read its logs.
					"kuberpack.dev/delivery": DeliveryLabel(req.DeliveryID),
					"kuberpack.dev/commit":   req.CommitSHA,
				}},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					NodeSelector:                 nodeSelector,
					Tolerations:                  tolerations,
					RuntimeClassName:             runtimeClassName,
					ServiceAccountName:           "kuberpack-build",
					AutomountServiceAccountToken: &never,
					ImagePullSecrets:             []corev1.LocalObjectReference{{Name: pullSecret}},
					SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					InitContainers: []corev1.Container{{
						Name: "buildkitd", Image: buildkitImage, ImagePullPolicy: corev1.PullIfNotPresent,
						Command: []string{"rootlesskit", "buildkitd"},
						// The cache of BuildKit is bounded: BuildKit deletes
						// the oldest entries when it grows past the limit.
						// Without it, the cache of a build fills the disk of
						// the node, and the next build fails for lack of
						// space.
						Args:            []string{"--addr=unix:///run/buildkit/buildkitd.sock", "--oci-worker-no-process-sandbox", "--oci-worker-gc-keepstorage=" + r.buildkitCacheLimit()},
						RestartPolicy:   &rootlessRestart,
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation, RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "buildkit-socket", MountPath: "/run/buildkit"}, {Name: "buildkit-cache", MountPath: "/home/user/.local/share/buildkit"}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: buildkitCPU, corev1.ResourceMemory: buildkitMemory}},
					}},
					Containers: []corev1.Container{{
						Name: "builder", Image: r.Image, ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/usr/bin/kuberpack-builder"},
						Env:             env,
						WorkingDir:      "/work",
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noEscalation, RunAsNonRoot: &allowEscalation, RunAsUser: &uid, RunAsGroup: &uid, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "buildkit-socket", MountPath: "/run/buildkit"}, {Name: "work", MountPath: "/work"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "tmp", MountPath: "/var/tmp"}, {Name: "secrets", MountPath: "/secrets", ReadOnly: true}, {Name: "trivy-cache", MountPath: "/work/trivy-cache"}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: builderCPU, corev1.ResourceMemory: builderMemory}},
					}},
					Volumes: []corev1.Volume{
						{Name: "buildkit-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "buildkit-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &cacheSize}}},
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &workSize}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpSize}}},
						{Name: "secrets", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret}}},
						// The database of the vulnerabilities is a cache, not
						// data: it is downloaded once and kept. Downloading
						// it at every build fills the disk of the node, and
						// the build fails for lack of space. The volume is a
						// claim of the chart: the group of the pod owns it,
						// and the builder may write in it.
						{Name: "trivy-cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "kuberpack-builder-cache",
						}}},
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

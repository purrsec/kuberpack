package builder

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const testSHA = "d56dc0764424bcdf1d4d2e6de8bb9e61c3334fa4"

func testRequest() Request {
	return Request{CloneURL: "https://git.host.bzh/pepe/hello-world.git", CommitSHA: testSHA, ImageRepository: "git.host.bzh/pepe/hello-world", RegistryUser: "pepe", BuildID: 42}
}

func TestNewJobIsolatedBuild(t *testing.T) {
	r := &JobRunner{Client: fake.NewSimpleClientset(), Image: "git.host.bzh/pepe/kuberpack-builder:0.1.0"}
	job, err := r.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if job.Namespace != "kuberpack-build" || job.Spec.Template.Spec.ServiceAccountName != "kuberpack-build" {
		t.Fatalf("wrong builder namespace or account: %#v", job.Spec.Template.Spec)
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("builder pod may access Kubernetes API")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatal("job must not silently retry a build")
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 || len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("expected separate BuildKit and builder containers")
	}
	buildkit := job.Spec.Template.Spec.InitContainers[0]
	if buildkit.RestartPolicy == nil || *buildkit.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("BuildKit must be a native sidecar")
	}
	if !strings.Contains(strings.Join(buildkit.Args, " "), "--oci-worker-no-process-sandbox") {
		t.Fatal("rootless BuildKit flags lost")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != r.Image || container.Command[1] != "builder" {
		t.Fatal("wrong builder image or command")
	}
	var writableVarTmp bool
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == "/var/tmp" && mount.Name == "tmp" && !mount.ReadOnly {
			writableVarTmp = true
		}
	}
	if !writableVarTmp {
		t.Fatal("Skopeo needs a writable /var/tmp")
	}
	for _, env := range container.Env {
		if strings.Contains(strings.ToLower(env.Name), "token") {
			t.Fatal("credentials must be mounted as files")
		}
	}
	if len(job.Spec.Template.Spec.Volumes) < 3 {
		t.Fatal("build workspace, socket and credentials must be mounted")
	}
}

func TestBuildWaitsForJobStatus(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		job.Name = "kuberpack-build-test"
		job.Status.Succeeded = 1
		return true, job, client.Tracker().Add(job)
	})
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	if err := r.Build(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	got, err := client.BatchV1().Jobs("kuberpack-build").Get(context.Background(), "kuberpack-build-test", metav1.GetOptions{})
	if err != nil || got.Spec.Template.Spec.Containers[0].Image != "builder:1" {
		t.Fatalf("Job not recorded: %v", err)
	}
}

func TestBuildReusesJobAfterRestart(t *testing.T) {
	req := testRequest()
	req.DeliveryID = "forgejo-delivery-42"
	client := fake.NewSimpleClientset()
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	job, err := r.NewJob(req)
	if err != nil {
		t.Fatal(err)
	}
	job.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs(job.Namespace).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	jobs, err := client.BatchV1().Jobs(job.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("restarted delivery created another Job: %v, %d", err, len(jobs.Items))
	}
}

func TestBuildFailedUsesTerminationMessage(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		job.Name = "kuberpack-build-test"
		job.Status.Failed = 1
		return true, job, client.Tracker().Add(job)
	})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "build-pod", Namespace: "kuberpack-build", Labels: map[string]string{"job-name": "kuberpack-build-test"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "builder",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "trivy: CVE-2025-68121 in stdlib (v1.22.12 → 1.24.13)"}},
		}}},
	}
	if err := client.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	err := r.Build(context.Background(), testRequest())
	if err == nil || err.Error() != "trivy: CVE-2025-68121 in stdlib (v1.22.12 → 1.24.13)" {
		t.Fatalf("got %v", err)
	}
}

func TestBuildFailedWaitsForTerminationMessage(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		job.Name = "kuberpack-build-test"
		job.Status.Failed = 1
		return true, job, client.Tracker().Add(job)
	})
	lists := 0
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		lists++
		msg := ""
		if lists > 1 {
			msg = "trivy: CVE-2020-14343 in PyYAML (5.3.1 → 5.4)"
		}
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "build-pod", Namespace: "kuberpack-build", Labels: map[string]string{"job-name": "kuberpack-build-test"}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "builder",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: msg}},
			}}},
		}}}, nil
	})
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	err := r.Build(context.Background(), testRequest())
	if err == nil || err.Error() != "trivy: CVE-2020-14343 in PyYAML (5.3.1 → 5.4)" {
		t.Fatalf("got %v after %d pod lists", err, lists)
	}
}

func TestNewJobPassesStartCommandAndRejectsChangedRequest(t *testing.T) {
	client := fake.NewSimpleClientset()
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	req := testRequest()
	req.DeliveryID = "forgejo-delivery-42"
	req.StartCmd = "gunicorn app:app --bind 0.0.0.0:$PORT"
	job, err := r.NewJob(req)
	if err != nil {
		t.Fatal(err)
	}
	var start string
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "KUBERPACK_START_CMD" {
			start = env.Value
		}
	}
	if start != req.StartCmd {
		t.Fatalf("start command not passed to worker: %q", start)
	}
	job.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs(job.Namespace).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	req.StartCmd = "python app.py"
	if err := r.Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "different request") {
		t.Fatalf("changed start command reused an old Job: %v", err)
	}
}

func TestBuildkitCacheIsBounded(t *testing.T) {
	r := &JobRunner{Client: fake.NewSimpleClientset(), Image: "git.host.bzh/pepe/kuberpack-builder:0.1.0"}
	job, err := r.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	args := job.Spec.Template.Spec.InitContainers[0].Args
	limit := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--oci-worker-gc-keepstorage=") {
			limit = arg
		}
	}
	if limit == "" {
		t.Fatalf("the cache of BuildKit is not bounded: %v", args)
	}
	r.BuildkitCache = "100,500,2000"
	job, err = r.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, arg := range job.Spec.Template.Spec.InitContainers[0].Args {
		if arg == "--oci-worker-gc-keepstorage=100,500,2000" {
			found = true
		}
	}
	if !found {
		t.Errorf("the room of the cache of the operator must be used: %v", job.Spec.Template.Spec.InitContainers[0].Args)
	}
}

func TestDeliveryLabelHoldsTheNameOfTheDelivery(t *testing.T) {
	short := "redeploy-comptoir-comptoir-0c74b39e-1de3d640"
	if got := DeliveryLabel(short); got != short {
		t.Fatalf("the name of a delivery that fits must be kept: %q", got)
	}
	long := "redeploy-comptoir-comptoir-0c74b39ef0f22cfa3bc09d0e2ab92acfa4c221b3-1de3d640f4071ecc"
	got := DeliveryLabel(long)
	if len(got) > 63 {
		t.Fatalf("a label holds 63 characters at most: %q", got)
	}
	if got == long {
		t.Fatal("a name that is too long must be shortened")
	}
	if got != DeliveryLabel(long) {
		t.Fatal("the same delivery must always give the same label")
	}
	if DeliveryLabel(long) == DeliveryLabel(long+"x") {
		t.Fatal("two deliveries must not share their label")
	}
	if DeliveryLabel("") != "" {
		t.Fatal("a build without delivery has no label")
	}
}

// The volumes of a build follow the disk of the node: a build on a small
// node gets small volumes.
func TestTheVolumesOfABuildFollowTheNode(t *testing.T) {
	runner := &JobRunner{NodeDisk: "20Gi"}
	work, cache, tmp := runner.volumeSizes()
	if work.Value() > 3<<30 {
		t.Errorf("the work volume is %v, it must follow the disk of the node", work)
	}
	if cache.Value() > 2<<30 {
		t.Errorf("the cache volume is %v, it must follow the disk of the node", cache)
	}
	if tmp.Value() > 1<<30 {
		t.Errorf("the tmp volume is %v, it must follow the disk of the node", tmp)
	}
	// A node whose size is not known keeps the default volumes.
	unknown := &JobRunner{}
	work, cache, tmp = unknown.volumeSizes()
	if work.String() != "3Gi" || cache.String() != "2Gi" || tmp.String() != "1Gi" {
		t.Errorf("the default volumes are %v, %v, %v", work, cache, tmp)
	}
}

func TestEvictedBuildReportsThePodReasonWhenContainerLogsAreGone(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "builds", Labels: map[string]string{"job-name": "build"}},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Message: `Usage of EmptyDir volume "buildkit-cache" exceeds the limit "1050M".`},
	})
	runner := &JobRunner{Client: client}
	message := runner.podFailureMessage(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "builds"}})
	if !strings.Contains(message, "Evicted") || !strings.Contains(message, "buildkit-cache") {
		t.Fatalf("the failure must preserve the eviction diagnosis: %q", message)
	}
}

func TestNativeBuildKitSidecarOOMExplainsTheDisconnectedBuilder(t *testing.T) {
	for _, restarted := range []bool{false, true} {
		status := corev1.ContainerStatus{Name: "buildkitd"}
		termination := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}
		if restarted {
			status.LastTerminationState.Terminated = termination
		} else {
			status.State.Terminated = termination
		}
		client := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "builds", Labels: map[string]string{"job-name": "build"}}, Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "buildkitd", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}}}}}, Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{status}, ContainerStatuses: []corev1.ContainerStatus{{Name: "builder", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "image: buildctl failed: exit status 1"}}}}}})
		message := (&JobRunner{Client: client}).podFailureMessage(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "builds"}})
		for _, part := range []string{"buildkitd", "OOMKilled", "2Gi"} {
			if !strings.Contains(message, part) {
				t.Fatalf("sidecar OOM lost: %s", message)
			}
		}
	}
}

func TestDiskAdmissionIncludesActiveBuildKitLayers(t *testing.T) {
	runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "registry.test/builder:1", NodeDisk: "35G"}
	job, err := runner.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var required, allocated int64
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "KUBERPACK_BUILD_DISK_NEED" {
			value, err := strconv.ParseInt(env.Value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			required = value
		}
	}
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.EmptyDir != nil && volume.EmptyDir.SizeLimit != nil {
			allocated += volume.EmptyDir.SizeLimit.Value()
			if volume.Name == "buildkit-cache" && volume.EmptyDir.SizeLimit.Value() < 2<<30 {
				t.Fatal("a normal Node build must not be evicted at the old 1050 MB cache limit")
			}
		}
	}
	if required == 0 || required != allocated {
		t.Fatalf("disk admission reserves %d bytes for %d bytes of bounded build volumes", required, allocated)
	}
}

package builder

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: "Trivy: CVE-2025-68121 in stdlib (v1.22.12 → 1.24.13)"}},
		}}},
	}
	if err := client.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	r := &JobRunner{Client: client, Image: "builder:1", PollInterval: time.Millisecond}
	err := r.Build(context.Background(), testRequest())
	if err == nil || err.Error() != "Trivy: CVE-2025-68121 in stdlib (v1.22.12 → 1.24.13)" {
		t.Fatalf("got %v", err)
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

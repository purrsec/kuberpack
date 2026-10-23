package provisioning

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeYAML "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

const admissionTestBuilderImage = "example.invalid/kuberpack-builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func renderBuildAdmission(t *testing.T, control, namespace, version string) ([]admissionv1.ValidatingAdmissionPolicy, []admissionv1.ValidatingAdmissionPolicyBinding, error) {
	t.Helper()
	chart, err := loader.Load("charts/kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ToRenderValues(chart, map[string]any{
		"builderImage": admissionTestBuilderImage, "buildNamespace": namespace,
	}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: control, IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: version}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, values)
	if err != nil {
		return nil, nil, err
	}
	// Flux Helm post-rendering parses the complete chart through Kustomize.
	// Catch YAML that Helm's template engine itself happily emits.
	var manifests strings.Builder
	for name, content := range files {
		if strings.HasSuffix(name, "NOTES.txt") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		manifests.WriteString(content)
		manifests.WriteString("\n---\n")
	}
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/rendered"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/rendered/kustomization.yaml", []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [chart.yaml]\n")); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/rendered/chart.yaml", []byte(manifests.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(fs, "/rendered"); err != nil {
		t.Fatalf("chart cannot pass Flux post-rendering: %v", err)
	}
	decoder := kubeYAML.NewYAMLOrJSONDecoder(strings.NewReader(files["kuberpack/templates/build-admission.yaml"]), 4096)
	var policies []admissionv1.ValidatingAdmissionPolicy
	var bindings []admissionv1.ValidatingAdmissionPolicyBinding
	for {
		var raw unstructured.Unstructured
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		data, err := raw.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		switch raw.GetKind() {
		case "ValidatingAdmissionPolicy":
			var policy admissionv1.ValidatingAdmissionPolicy
			if err := kubeYAML.Unmarshal(data, &policy); err != nil {
				t.Fatal(err)
			}
			policies = append(policies, policy)
		case "ValidatingAdmissionPolicyBinding":
			var binding admissionv1.ValidatingAdmissionPolicyBinding
			if err := kubeYAML.Unmarshal(data, &binding); err != nil {
				t.Fatal(err)
			}
			bindings = append(bindings, binding)
		}
	}
	return policies, bindings, nil
}

func TestBuildAdmissionRequiresSupportedAPIServer(t *testing.T) {
	if _, _, err := renderBuildAdmission(t, "control", "build", "v1.29.15"); err == nil {
		t.Fatal("builder admission silently accepted an unsupported API server")
	}
	policies, bindings, err := renderBuildAdmission(t, "control", "build", "v1.30.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 2 || len(bindings) != 2 {
		t.Fatal("both Jobs and Pods must be protected")
	}
}

// Le chart doit transmettre le placement au control plane et l'épingler dans
// l'admission. Sans évaluation CEL locale, le test vérifie le rendu exact des
// deux côtés : un écart ferait refuser les Jobs légitimes.
func TestBuildPlacementIsRenderedOnBothSides(t *testing.T) {
	chart, err := loader.Load("charts/kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ToRenderValues(chart, map[string]any{
		"builderImage": admissionTestBuilderImage, "buildNamespace": "build",
		"builderPlacement": map[string]any{
			"runtimeClass": "gvisor",
			"nodeSelector": map[string]any{"kuberpack.dev/build": "true"},
			"tolerations": []any{map[string]any{
				"key": "dedicated", "operator": "Equal", "value": "build", "effect": "NoSchedule",
			}},
		},
	}, chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "control", IsInstall: true}, &chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.37.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, values)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"KUBERPACK_BUILDER_RUNTIME_CLASS",
		`value: "gvisor"`,
		"KUBERPACK_BUILDER_NODE_SELECTOR",
		`value: "{\"kuberpack.dev/build\":\"true\"}"`,
		"KUBERPACK_BUILDER_TOLERATIONS",
	} {
		if !strings.Contains(files["kuberpack/templates/deployment.yaml"], want) {
			t.Errorf("le control plane ne reçoit pas le placement: %s", want)
		}
	}
	for _, want := range []string{
		`variables.p.runtimeClassName == "gvisor"`,
		`variables.p.nodeSelector == {"kubernetes.io/arch":"amd64","kuberpack.dev/build":"true"}`,
		"variables.p.tolerations.size() <= 16",
		"has(t.key) && t.key != ''",
	} {
		if !strings.Contains(files["kuberpack/templates/build-admission.yaml"], want) {
			t.Errorf("l'admission n'épingle pas le placement: %s", want)
		}
	}
}

// This opt-in test talks only to the disposable Docker cluster. Test objects
// are submitted with dry-run, so no untrusted container starts. No Secret is
// read or created; the admission assertions use only names of fake credentials.
func TestBuildAdmissionLiveAPIServer(t *testing.T) {
	file := os.Getenv("KUBERPACK_SECURITY_KUBECONFIG")
	if file == "" {
		t.Skip("set KUBERPACK_SECURITY_KUBECONFIG to the disposable Docker cluster kubeconfig")
	}
	config, err := clientcmd.BuildConfigFromFlags("", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(config.Host, "/") != "https://172.29.90.11:6443" {
		t.Fatal("security admission test is restricted to the disposable Docker cluster")
	}
	config.Timeout = 15 * time.Second
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "kuberpack-admission-audit-", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.CoreV1().Namespaces().Delete(cleanup, ns.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup namespace: %v", err)
		}
	})
	_, err = client.CoreV1().ServiceAccounts(ns.Name).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "kuberpack-build"}, AutomountServiceAccountToken: ptr.To(false)}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policies, bindings, err := renderBuildAdmission(t, ns.Name, ns.Name, "v1.37.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, desired := range policies {
		policy, err := client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, &desired, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		name := policy.Name
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Delete(cleanup, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup policy: %v", err)
			}
		})
		if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(ctx context.Context) (bool, error) {
			observed, err := client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if observed.Status.TypeChecking == nil {
				return false, nil
			}
			if len(observed.Status.TypeChecking.ExpressionWarnings) != 0 {
				return false, fmt.Errorf("CEL type warnings for %s: %+v", name, observed.Status.TypeChecking.ExpressionWarnings)
			}
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range bindings {
		if _, err := client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, &binding, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		name := binding.Name
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Delete(cleanup, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup binding: %v", err)
			}
		})
	}
	runner := builder.JobRunner{Client: client, Namespace: ns.Name, Image: admissionTestBuilderImage}
	base, err := runner.NewJob(builder.Request{CloneURL: "https://example.invalid/source.git", CommitSHA: strings.Repeat("a", 40), ImageRepository: "example.invalid/app", RegistryUser: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Poll a forbidden request first, since admission bindings propagate
	// asynchronously through the API server's informer caches.
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 15*time.Second, true, func(ctx context.Context) (bool, error) {
		bad := base.DeepCopy()
		bad.Spec.Template.Spec.HostPID = true
		_, err := client.BatchV1().Jobs(ns.Name).Create(ctx, bad, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err == nil {
			return false, nil
		}
		if !apierrors.IsForbidden(err) {
			return false, err
		}
		return strings.Contains(err.Error(), ns.Name+"-kuberpack-build-jobs"), nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Job", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			submit := func(spec corev1.PodSpec) error {
				options := metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
				if kind == "Job" {
					job := base.DeepCopy()
					job.Spec.Template.Spec = spec
					_, err := client.BatchV1().Jobs(ns.Name).Create(ctx, job, options)
					return err
				}
				_, err := client.CoreV1().Pods(ns.Name).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "admission-dry-run-"}, Spec: spec}, options)
				return err
			}
			if err := submit(*base.Spec.Template.Spec.DeepCopy()); err != nil {
				t.Fatalf("JobRunner.NewJob was rejected: %v", err)
			}
			for _, test := range buildAdmissionAttacks() {
				t.Run(test.name, func(t *testing.T) {
					spec := base.Spec.Template.Spec.DeepCopy()
					test.mutate(spec)
					err := submit(*spec)
					if err == nil {
						t.Fatal("unsafe build specification was admitted")
					}
					if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), ns.Name+"-kuberpack-build-") {
						t.Fatalf("expected the build admission policy to reject this request, got %v", err)
					}
				})
			}
		})
	}
	t.Run("Pod updates and ephemeral-container subresource", func(t *testing.T) {
		spec := base.Spec.Template.Spec.DeepCopy()
		spec.NodeSelector = map[string]string{"kuberpack.audit/scheduling": ns.Name}
		pod, err := client.CoreV1().Pods(ns.Name).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "admission-pending-"}, Spec: *spec}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// This Pod remains unscheduled; fake images and credentials are never
		// consumed. The test namespace cleanup removes it after all assertions.
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			changed, err := client.CoreV1().Pods(ns.Name).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			changed.Spec.Containers[0].Image = "example.invalid/evil:latest"
			_, err = client.CoreV1().Pods(ns.Name).Update(ctx, changed, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), ns.Name+"-kuberpack-build-pods") {
			t.Fatalf("unsafe Pod UPDATE escaped admission: %v", err)
		}
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			changed, err := client.CoreV1().Pods(ns.Name).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			changed.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debugger", Image: "example.invalid/debugger:latest", Command: []string{"sh"}}, TargetContainerName: "builder"}}
			_, err = client.CoreV1().Pods(ns.Name).UpdateEphemeralContainers(ctx, pod.Name, changed, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), ns.Name+"-kuberpack-build-pods") {
			t.Fatalf("ephemeral-container subresource escaped admission: %v", err)
		}
	})
}

type buildAdmissionAttack struct {
	name   string
	mutate func(*corev1.PodSpec)
}

func buildAdmissionAttacks() []buildAdmissionAttack {
	return []buildAdmissionAttack{
		{"hostPath", func(p *corev1.PodSpec) {
			p.Volumes = append(p.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		}},
		{"privileged builder", func(p *corev1.PodSpec) {
			p.Containers[0].SecurityContext.Privileged = ptr.To(true)
			p.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr.To(true)
		}},
		{"privileged buildkit", func(p *corev1.PodSpec) { p.InitContainers[0].SecurityContext.Privileged = ptr.To(true) }},
		{"host network", func(p *corev1.PodSpec) { p.HostNetwork = true }},
		{"host PID", func(p *corev1.PodSpec) { p.HostPID = true }},
		{"host IPC", func(p *corev1.PodSpec) { p.HostIPC = true }},
		{"shared process namespace", func(p *corev1.PodSpec) { p.ShareProcessNamespace = ptr.To(true) }},
		{"another runtime class", func(p *corev1.PodSpec) { p.RuntimeClassName = ptr.To("runc") }},
		{"another node pool", func(p *corev1.PodSpec) {
			p.NodeSelector = map[string]string{"kubernetes.io/arch": "arm64"}
		}},
		{"wildcard toleration", func(p *corev1.PodSpec) {
			p.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
		}},
		{"another service account", func(p *corev1.PodSpec) { p.ServiceAccountName = "default" }},
		{"automount token", func(p *corev1.PodSpec) { p.AutomountServiceAccountToken = ptr.To(true) }},
		{"implicit automount token", func(p *corev1.PodSpec) { p.AutomountServiceAccountToken = nil }},
		{"projected service account token", func(p *corev1.PodSpec) {
			p.Volumes = append(p.Volumes, corev1.Volume{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}}}}})
		}},
		{"another builder image", func(p *corev1.PodSpec) { p.Containers[0].Image = "example.invalid/evil:latest" }},
		{"another buildkit image", func(p *corev1.PodSpec) { p.InitContainers[0].Image = "example.invalid/evil:latest" }},
		{"another builder command", func(p *corev1.PodSpec) { p.Containers[0].Command = []string{"sh", "-c", "sleep 600"} }},
		{"another buildkit command", func(p *corev1.PodSpec) { p.InitContainers[0].Command = []string{"sh", "-c", "sleep 600"} }},
		{"builder arguments", func(p *corev1.PodSpec) { p.Containers[0].Args = []string{"serve"} }},
		{"buildkit insecure entitlement", func(p *corev1.PodSpec) {
			p.InitContainers[0].Args = append(p.InitContainers[0].Args, "--allow-insecure-entitlement=security.insecure")
		}},
		{"buildkit TCP endpoint", func(p *corev1.PodSpec) { p.InitContainers[0].Args[0] = "--addr=tcp://0.0.0.0:1234" }},
		{"buildkit arbitrary configuration", func(p *corev1.PodSpec) { p.InitContainers[0].Args[2] = "--config=/secrets/config" }},
		{"missing init container", func(p *corev1.PodSpec) { p.InitContainers = nil }},
		{"empty init containers", func(p *corev1.PodSpec) { p.InitContainers = []corev1.Container{} }},
		{"extra init container", func(p *corev1.PodSpec) {
			extra := p.InitContainers[0].DeepCopy()
			extra.Name = "extra"
			p.InitContainers = append(p.InitContainers, *extra)
		}},
		{"extra application container", func(p *corev1.PodSpec) {
			extra := p.Containers[0].DeepCopy()
			extra.Name = "extra"
			p.Containers = append(p.Containers, *extra)
		}},
		{"another secret volume", func(p *corev1.PodSpec) { p.Volumes[4].Secret.SecretName = "another-secret" }},
		{"another cache PVC", func(p *corev1.PodSpec) { p.Volumes[5].PersistentVolumeClaim.ClaimName = "another-claim" }},
		{"another image pull secret", func(p *corev1.PodSpec) { p.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "another-secret"}} }},
		{"env secret reference", func(p *corev1.PodSpec) {
			p.Containers[0].Env = append(p.Containers[0].Env, corev1.EnvVar{Name: "OTHER_SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "another-secret"}, Key: "password"}}})
		}},
		{"envFrom secret reference", func(p *corev1.PodSpec) {
			p.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "another-secret"}}}}
		}},
		{"added capability", func(p *corev1.PodSpec) {
			p.InitContainers[0].SecurityContext.Capabilities = &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}
		}},
		{"builder root", func(p *corev1.PodSpec) { p.Containers[0].SecurityContext.RunAsUser = ptr.To(int64(0)) }},
		{"pod root", func(p *corev1.PodSpec) { p.SecurityContext.RunAsUser = ptr.To(int64(0)) }},
		{"unconfined pod seccomp", func(p *corev1.PodSpec) { p.SecurityContext.SeccompProfile.Type = corev1.SeccompProfileTypeUnconfined }},
		{"unconfined builder seccomp", func(p *corev1.PodSpec) {
			p.Containers[0].SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
		}},
		{"builder privilege escalation", func(p *corev1.PodSpec) { p.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr.To(true) }},
		{"writable builder root", func(p *corev1.PodSpec) { p.Containers[0].SecurityContext.ReadOnlyRootFilesystem = ptr.To(false) }},
		{"retained builder capabilities", func(p *corev1.PodSpec) { p.Containers[0].SecurityContext.Capabilities.Drop = nil }},
		{"missing builder security context", func(p *corev1.PodSpec) { p.Containers[0].SecurityContext = nil }},
		{"missing pod security context", func(p *corev1.PodSpec) { p.SecurityContext = nil }},
		{"host port", func(p *corev1.PodSpec) {
			p.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 8080, HostPort: 8080}}
		}},
		{"custom sysctl", func(p *corev1.PodSpec) {
			p.SecurityContext.Sysctls = []corev1.Sysctl{{Name: "net.ipv4.ip_local_port_range", Value: "1024 65535"}}
		}},
		{"lifecycle executable", func(p *corev1.PodSpec) {
			p.Containers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "true"}}}}
		}},
		{"exec probe", func(p *corev1.PodSpec) {
			p.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "true"}}}}
		}},
	}
}

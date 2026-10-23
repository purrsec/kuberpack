package netcheck

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func daemonSet(name string, containers ...corev1.Container) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}}},
	}
}

func container(name, image string, env map[string]string, args ...string) corev1.Container {
	candidate := corev1.Container{Name: name, Image: image, Args: args}
	for key, value := range env {
		candidate.Env = append(candidate.Env, corev1.EnvVar{Name: key, Value: value})
	}
	return candidate
}

func configMap(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"}, Data: data}
}

func node(annotations map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Annotations: annotations}}
}

func detectForTest(t *testing.T, kubeVersion string, objects ...runtime.Object) (Detection, error) {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	if kubeVersion != "" {
		fakeDiscovery, ok := client.Discovery().(*fakediscovery.FakeDiscovery)
		if !ok {
			t.Fatal("fake discovery inattendu")
		}
		fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: kubeVersion}
	}
	return Detect(context.Background(), client)
}

func TestDetectIdentifiesNetworkEngines(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		objects     []runtime.Object
		engine      Engine
		enforcement Enforcement
		mode        string
		warning     string
	}{
		{
			name:        "k3s intègre kube-router",
			version:     "v1.37.1+k3s1",
			engine:      EngineKubeRouter,
			enforcement: EnforcementExpected,
			warning:     "kube-router",
		},
		{
			name:        "calico",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("calico-node", container("calico-node", "docker.io/calico/node:v3.29.1", nil))},
			engine:      EngineCalico,
			enforcement: EnforcementExpected,
			warning:     "GlobalNetworkPolicy",
		},
		{
			name:        "cilium en mode audit",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("cilium", container("cilium-agent", "quay.io/cilium/cilium:v1.16.0", nil)), configMap("cilium-config", map[string]string{"policy-enforcement": "audit"})},
			engine:      EngineCilium,
			enforcement: EnforcementAudit,
			warning:     "audit",
		},
		{
			name:        "aws cni agent désactivé puis réactivé",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("aws-node", container("aws-node", "amazon/amazon-k8s-cni:v1.18.0", map[string]string{"NETWORK_POLICY_ENFORCING_MODE": "standard"}), container("aws-eks-nodeagent", "amazon/aws-network-policy-agent:v1.1.0", nil, "--enable-network-policy=false")), configMap("amazon-vpc-cni", map[string]string{"enable-network-policy-controller": "false"})},
			engine:      EngineAWSVPC,
			enforcement: EnforcementDisabled,
			mode:        "standard",
			warning:     "désactivé",
		},
		{
			name:        "aws cni strict avec agent actif",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("aws-node", container("aws-node", "amazon/amazon-k8s-cni:v1.18.0", map[string]string{"NETWORK_POLICY_ENFORCING_MODE": "strict"}), container("aws-eks-nodeagent", "amazon/aws-network-policy-agent:v1.1.0", nil, "--enable-network-policy=true")), configMap("amazon-vpc-cni", map[string]string{"enable-network-policy-controller": "true"})},
			engine:      EngineAWSVPC,
			enforcement: EnforcementExpected,
			mode:        "strict",
			warning:     "strict",
		},
		{
			name:        "aws cni argument actif prioritaire sur la configmap",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("aws-node", container("aws-node", "amazon/amazon-k8s-cni:v1.18.0", map[string]string{"NETWORK_POLICY_ENFORCING_MODE": "strict"}), container("aws-eks-nodeagent", "amazon/aws-network-policy-agent:v1.1.0", nil, "--enable-network-policy=true")), configMap("amazon-vpc-cni", map[string]string{"enable-network-policy-controller": "false"})},
			engine:      EngineAWSVPC,
			enforcement: EnforcementExpected,
			mode:        "strict",
			warning:     "strict",
		},
		{
			name:        "azure npm",
			version:     "v1.30.4",
			objects:     []runtime.Object{daemonSet("azure-npm", container("azure-npm", "mcr.microsoft.com/containernetworking/azure-npm:v1.5.0", nil))},
			engine:      EngineAzureNPM,
			enforcement: EnforcementExpected,
			warning:     "AKS",
		},
		{
			name:        "moteur inconnu",
			version:     "v1.30.4",
			engine:      EngineUnknown,
			enforcement: EnforcementUnknown,
			warning:     "non identifié",
		},
		{
			name:        "k3s avec network policy désactivée",
			version:     "v1.37.1+k3s1",
			objects:     []runtime.Object{node(map[string]string{"k3s.io/node-args": `["server","--disable-network-policy"]`})},
			engine:      EngineKubeRouter,
			enforcement: EnforcementDisabled,
			warning:     "--disable-network-policy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection, err := detectForTest(t, test.version, test.objects...)
			if err != nil {
				t.Fatalf("détection: %v", err)
			}
			if detection.Engine != test.engine {
				t.Fatalf("moteur = %s, attendu %s", detection.Engine, test.engine)
			}
			if detection.Enforcement != test.enforcement {
				t.Fatalf("application = %s, attendu %s", detection.Enforcement, test.enforcement)
			}
			if detection.Mode != test.mode {
				t.Fatalf("mode = %q, attendu %q", detection.Mode, test.mode)
			}
			if !strings.Contains(strings.Join(detection.Warnings, " "), test.warning) {
				t.Fatalf("avertissements %q sans %q", detection.Warnings, test.warning)
			}
		})
	}
}

func TestDetectWarnsAboutSeveralEngines(t *testing.T) {
	detection, err := detectForTest(t, "v1.30.4",
		daemonSet("calico-node", container("calico-node", "calico/node:v3.29.1", nil)),
		daemonSet("cilium", container("cilium-agent", "cilium:v1.16.0", nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Engine != EngineCalico {
		t.Fatalf("priorité inattendue: %s", detection.Engine)
	}
	if !strings.Contains(strings.Join(detection.Warnings, " "), "plusieurs moteurs") {
		t.Fatalf("conflit non signalé: %v", detection.Warnings)
	}
}

// La détection ne doit jamais toucher aux Secrets, même pour lire une
// configuration de CNI.
func TestDetectReadsNoSecret(t *testing.T) {
	client := fake.NewSimpleClientset(
		daemonSet("aws-node", container("aws-node", "amazon-k8s-cni:v1.18.0", map[string]string{"NETWORK_POLICY_ENFORCING_MODE": "standard"})),
		configMap("amazon-vpc-cni", map[string]string{"enable-network-policy-controller": "true"}),
	)
	if _, err := Detect(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if strings.Contains(action.GetResource().Resource, "secret") {
			t.Fatalf("la détection a lu une ressource Secret: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestDetectK3sEvidenceComesFromServerVersion(t *testing.T) {
	detection, err := detectForTest(t, "v1.37.1+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(detection.Evidence, " "), "+k3s") {
		t.Fatalf("preuve k3s absente: %v", detection.Evidence)
	}
}

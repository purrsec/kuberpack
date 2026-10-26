package netcheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type scriptedRunner struct {
	outcomes map[string]Outcome
	failures map[string]error
	calls    []string
}

func (r *scriptedRunner) Run(_ context.Context, _ string, job *batchv1.Job) (Outcome, error) {
	r.calls = append(r.calls, job.Name)
	if err := r.failures[job.Name]; err != nil {
		return OutcomeInconclusive, err
	}
	outcome, ok := r.outcomes[job.Name]
	if !ok {
		return OutcomeInconclusive, fmt.Errorf("job inattendu: %s", job.Name)
	}
	return outcome, nil
}

func probeClient(namespace string, extra ...runtime.Object) *fake.Clientset {
	objects := []runtime.Object{&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "target-pod", Namespace: namespace, Labels: map[string]string{probeLabel: "target"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.10"},
	}}
	objects = append(objects, extra...)
	return fake.NewSimpleClientset(objects...)
}

func TestRunProbeProvesDenyAll(t *testing.T) {
	const namespace = "netcheck-test"
	client := probeClient(namespace)
	runner := &scriptedRunner{outcomes: map[string]Outcome{
		"netcheck-client-allow":  OutcomeReachable,
		"netcheck-client-deny-1": OutcomeBlocked,
	}}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != VerdictEnforced || report.Positive != OutcomeReachable || report.Negative != OutcomeBlocked {
		t.Fatalf("verdict inattendu: %+v", report)
	}
	if report.Cleanup != "namespace supprimé" || report.CleanupFailed {
		t.Fatalf("nettoyage inattendu: %q", report.Cleanup)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("probes exécutées: %v", runner.calls)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace temporaire non supprimé: %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies(namespace).Get(context.Background(), "netcheck-deny-all", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.PodSelector.MatchLabels) != 0 {
		t.Fatalf("le deny-all doit sélectionner tous les pods: %v", policy.Spec.PodSelector)
	}
	if len(policy.Spec.PolicyTypes) != 2 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress || policy.Spec.PolicyTypes[1] != networkingv1.PolicyTypeEgress {
		t.Fatalf("policyTypes inattendus: %v", policy.Spec.PolicyTypes)
	}
	for _, action := range client.Actions() {
		if strings.Contains(action.GetResource().Resource, "secret") {
			t.Fatalf("la probe a lu ou écrit un Secret: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestRunProbeReportsUnenforcedPolicy(t *testing.T) {
	const namespace = "netcheck-test"
	client := probeClient(namespace)
	runner := &scriptedRunner{outcomes: map[string]Outcome{
		"netcheck-client-allow":  OutcomeReachable,
		"netcheck-client-deny-1": OutcomeReachable,
		"netcheck-client-deny-2": OutcomeReachable,
		"netcheck-client-deny-3": OutcomeReachable,
	}}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != VerdictNotEnforced {
		t.Fatalf("verdict = %s, attendu %s", report.Verdict, VerdictNotEnforced)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("tentatives deny-all: %v", runner.calls)
	}
}

func TestRunProbeRefusesToConcludeWhenBaselineFails(t *testing.T) {
	const namespace = "netcheck-test"
	client := probeClient(namespace)
	runner := &scriptedRunner{outcomes: map[string]Outcome{"netcheck-client-allow": OutcomeBlocked}}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, attendu indéterminé", report.Verdict)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("la probe ne doit pas créer de deny-all après un contrôle positif raté: %v", runner.calls)
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "networkpolicies" {
			t.Fatal("une NetworkPolicy a été créée malgré un contrôle positif raté")
		}
	}
}

func TestRunProbeRefusesExistingNamespace(t *testing.T) {
	const namespace = "netcheck-test"
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	runner := &scriptedRunner{}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "existe déjà") {
		t.Fatalf("namespace existant accepté: %v", err)
	}
	if report.Namespace != namespace || len(runner.calls) != 0 {
		t.Fatalf("rapport inattendu: %+v", report)
	}
}

func TestRunProbeReportsCleanupFailure(t *testing.T) {
	const namespace = "netcheck-test"
	client := probeClient(namespace)
	client.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("suppression refusée")
	})
	runner := &scriptedRunner{outcomes: map[string]Outcome{
		"netcheck-client-allow":  OutcomeReachable,
		"netcheck-client-deny-1": OutcomeBlocked,
	}}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if !report.CleanupFailed || !strings.Contains(report.Cleanup, "échec") {
		t.Fatalf("échec de nettoyage non signalé: %+v", report)
	}
}

func TestRunProbeRecordsRunnerFailure(t *testing.T) {
	const namespace = "netcheck-test"
	client := probeClient(namespace)
	runner := &scriptedRunner{
		outcomes: map[string]Outcome{},
		failures: map[string]error{"netcheck-client-allow": errors.New("image introuvable")},
	}
	report, err := RunProbe(context.Background(), client, ProbeOptions{Namespace: namespace, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, attendu indéterminé", report.Verdict)
	}
	if !strings.Contains(strings.Join(report.Notes, " "), "image introuvable") {
		t.Fatalf("erreur du runner absente du rapport: %v", report.Notes)
	}
}

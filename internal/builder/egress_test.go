package builder

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// Le builder clone le dépôt de l'application : il doit recevoir l'allowlist
// d'egress du control plane, sinon une forge interne serait refusée.
func TestBuilderJobCarriesTheEgressAllowlist(t *testing.T) {
	runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "git.host.bzh/pepe/kuberpack-builder:0.1.0"}
	request := testRequest()
	request.EgressAllow = "10.20.0.0/16,git.interne"
	job, err := runner.NewJob(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range job.Spec.Template.Spec.Containers {
		for _, variable := range container.Env {
			if variable.Name != "KUBERPACK_EGRESS_ALLOW" {
				continue
			}
			if variable.Value != request.EgressAllow {
				t.Fatalf("allowlist transmise: %q", variable.Value)
			}
			return
		}
	}
	t.Fatal("le Job du builder ne porte pas KUBERPACK_EGRESS_ALLOW")
}

func TestBuilderJobOmitsAnEmptyEgressAllowlist(t *testing.T) {
	runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "git.host.bzh/pepe/kuberpack-builder:0.1.0"}
	job, err := runner.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range job.Spec.Template.Spec.Containers {
		for _, variable := range container.Env {
			if variable.Name == "KUBERPACK_EGRESS_ALLOW" {
				t.Fatalf("une allowlist vide ne doit pas être transmise: %q", variable.Value)
			}
		}
	}
}

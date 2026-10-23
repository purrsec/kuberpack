package builder

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestPublicBuildExplicitlyClonesAnonymously(t *testing.T) {
	runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "test-builder"}
	req := testRequest()
	req.CloneAnonymous = true
	job, err := runner.NewJob(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, variable := range job.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "KUBERPACK_GIT_ANONYMOUS" && variable.Value == "true" {
			return
		}
	}
	t.Fatal("builder was not told to clone without credentials")
}

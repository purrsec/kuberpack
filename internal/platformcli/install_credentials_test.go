package platformcli

import (
	"context"
	"testing"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestInstallationNeverGivesItsGitOpsWriterToTheBuilder(t *testing.T) {
	for _, c := range []installCredentials{
		{Git: "writer", Registry: "writer"},
		{Git: "writer", Registry: "push", BuilderGit: "writer"},
		{Git: "writer", Registry: "push", BuilderGit: "push"},
		{Git: "writer", Registry: "push", API: "writer"},
	} {
		i := &installation{kube: fake.NewSimpleClientset(), credentials: c}
		if err := i.prepareCredentials(context.Background()); err == nil {
			t.Fatal("shared authority accepted")
		}
		if len(i.kube.(*fake.Clientset).Actions()) != 0 {
			t.Fatal("credentials were written before rejecting shared authority")
		}
	}
	for _, reader := range []string{"", "source-reader"} {
		i := &installation{c: setup.Config{Namespace: "control", BuildNamespace: "build", RegistryURL: "registry.example.org", RegistryUser: "builder"}, kube: fake.NewSimpleClientset(), credentials: installCredentials{Git: "writer", Registry: "push", BuilderGit: reader}}
		if err := i.prepareCredentials(context.Background()); err != nil {
			t.Fatal(err)
		}
		secret, err := i.kube.CoreV1().Secrets("build").Get(context.Background(), "kuberpack-builder", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if string(secret.Data["git-token"]) != reader || string(secret.Data["registry-token"]) != "push" {
			t.Fatal("builder credential roles were mixed")
		}
	}
}

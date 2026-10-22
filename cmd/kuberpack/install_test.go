package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The installation must be doable with two tokens and the URL of the
// GitOps repository: everything else is deduced from it.
func TestTheForgeAndTheRegistryComeFromTheGitOpsURL(t *testing.T) {
	cases := []struct {
		name         string
		repository   string
		forge        string
		user         string
		registry     string
		registryUser string
	}{
		{
			name:         "github",
			repository:   "https://github.com/purrsec/kuberpack-infra",
			forge:        "https://github.com",
			user:         "purrsec",
			registry:     "ghcr.io",
			registryUser: "purrsec",
		},
		{
			name:         "a self hosted forge",
			repository:   "https://git.example.com/team/infra.git",
			forge:        "https://git.example.com",
			user:         "team",
			registry:     "https://git.example.com",
			registryUser: "team",
		},
		{
			name:         "gitlab",
			repository:   "https://gitlab.com/group/subgroup/infra",
			forge:        "https://gitlab.com",
			user:         "group",
			registry:     "https://gitlab.com",
			registryUser: "group",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forgeBaseURL(c.repository); got != c.forge {
				t.Errorf("forgeBaseURL(%q) = %q, want %q", c.repository, got, c.forge)
			}
			if got := forgeUser(c.repository); got != c.user {
				t.Errorf("forgeUser(%q) = %q, want %q", c.repository, got, c.user)
			}
			registry, user := registryOf(c.forge, c.user)
			if registry != c.registry {
				t.Errorf("registryOf(%q) = %q, want %q", c.forge, registry, c.registry)
			}
			if user != c.registryUser {
				t.Errorf("registryOf(%q) user = %q, want %q", c.forge, user, c.registryUser)
			}
		})
	}
}

// A URL that says nothing must not be guessed.
func TestTheForgeOfAnUnreadableURLIsEmpty(t *testing.T) {
	if got := forgeBaseURL(""); got != "" {
		t.Errorf("forgeBaseURL(\"\") = %q, want empty", got)
	}
	if got := forgeUser(""); got != "" {
		t.Errorf("forgeUser(\"\") = %q, want empty", got)
	}
	if got, user := registryOf("", ""); got != "" || user != "" {
		t.Errorf("registryOf(\"\", \"\") = %q, %q, want empty", got, user)
	}
}

// The installation is run again on a cluster that is already prepared: the
// type of the Secrets must not change. Kubernetes refuses it, and the
// installation would fail on a cluster that works.
func TestTheSecretKeepsItsTypeWhenTheInstallationRunsAgain(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuberpack-builder-pull", Namespace: "kuberpack-build"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte("{}")},
	})
	if err := ensureSecret(ctx, client, "kuberpack-build", "kuberpack-builder-pull", map[string][]byte{
		corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`),
	}); err != nil {
		t.Fatalf("ensureSecret: %v", err)
	}
	secret, err := client.CoreV1().Secrets("kuberpack-build").Get(ctx, "kuberpack-builder-pull", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("type = %q, want %q", secret.Type, corev1.SecretTypeDockerConfigJson)
	}
	if got := string(secret.Data[corev1.DockerConfigJsonKey]); got != `{"auths":{}}` {
		t.Errorf("data = %q, want the new content", got)
	}
}

// A Secret that is created must have the type its data demands.
func TestASecretThatPullsImagesHasTheTypeOfADockerConfig(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	if err := ensureSecret(ctx, client, "kuberpack-build", "kuberpack-builder-pull", map[string][]byte{
		corev1.DockerConfigJsonKey: []byte("{}"),
	}); err != nil {
		t.Fatalf("ensureSecret: %v", err)
	}
	secret, err := client.CoreV1().Secrets("kuberpack-build").Get(ctx, "kuberpack-builder-pull", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("type = %q, want %q", secret.Type, corev1.SecretTypeDockerConfigJson)
	}
}

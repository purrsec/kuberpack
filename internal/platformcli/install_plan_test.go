package platformcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	setup "git.host.bzh/pepe/kuberpack/internal/install"
	"helm.sh/helm/v3/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHelmResourcesUseTheReleaseNamespaceInsteadOfTheContextDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	os.WriteFile(file, []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: https://127.0.0.1:6443\ncontexts:\n- name: test\n  context:\n    cluster: test\n    namespace: default\ncurrent-context: test\n"), 0600)
	settings := cli.New()
	settings.KubeConfig = file
	i := &installation{settings: settings}
	_, err := i.helmConfiguration("infisical")
	if err != nil {
		t.Fatal(err)
	}
	namespace, _, err := settings.RESTClientGetter().ToRawKubeConfigLoader().Namespace()
	if err != nil || namespace != "infisical" {
		t.Fatalf("namespace=%s err=%v", namespace, err)
	}
}

func TestInstallationCredentialsSurviveReentryAndNoopDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	count := 0
	generate := func() map[string][]byte { count++; return map[string][]byte{"password": []byte("one-password")} }
	first, err := persistentSecret(ctx, client.CoreV1().Secrets("test"), "credentials", generate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := persistentSecret(ctx, client.CoreV1().Secrets("test"), "credentials", generate)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || string(first.Data["password"]) != string(second.Data["password"]) {
		t.Fatal("credentials were regenerated")
	}
	client.ClearActions()
	if err := ensureSecret(ctx, client, "test", "credentials", map[string][]byte{"password": []byte("one-password")}); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("no-op reinstall updated the Secret")
		}
	}
	if err := ensureSecret(ctx, client, "test", "credentials", map[string][]byte{"other": []byte("second-key")}); err != nil {
		t.Fatal(err)
	}
	secret, _ := client.CoreV1().Secrets("test").Get(ctx, "credentials", metav1.GetOptions{})
	if string(secret.Data["password"]) != "one-password" {
		t.Fatal("unrelated credential removed")
	}
}

func TestInstallationCannotStealActiveLease(t *testing.T) {
	i := &installation{c: setup.Config{Namespace: "test"}, kube: fake.NewSimpleClientset()}
	ctx, unlock, err := i.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := i.lock(context.Background()); err == nil {
		t.Fatal("concurrent installer acquired lock")
	}
	unlock()
	if ctx.Err() == nil {
		t.Fatal("installation context still live")
	}
	_, release, err := i.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestInstallerPreservesHelmfileAndRejectsSymlinkWrites(t *testing.T) {
	root := t.TempDir()
	helmfile := filepath.Join(root, "helmfile.yaml")
	original := []byte("releases: [{name: custom}]\n")
	if err := os.WriteFile(helmfile, original, 0600); err != nil {
		t.Fatal(err)
	}
	i := &installation{work: root, c: setup.Config{Root: "clusters/test/kuberpack"}}
	if err := i.copyCharts(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(helmfile)
	if string(data) != string(original) {
		t.Fatal("Helmfile was modified")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "clusters/test/kuberpack/projects")); err != nil {
		t.Fatal(err)
	}
	if err := i.writeBytes("clusters/test/kuberpack/projects/stolen.yaml", []byte("bad")); err == nil {
		t.Fatal("followed a repository symlink")
	}
	if err := i.writeBytes("helmfile.yaml", []byte("bad")); err == nil {
		t.Fatal("wrote outside generated scope")
	}
}

func TestPlatformConfigurationRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	for _, data := range []string{"cluster: test\napiToken: do-not-put-secrets-here\n", "cluster: test\n---\ncluster: other\n"} {
		file := filepath.Join(t.TempDir(), "platform.yaml")
		os.WriteFile(file, []byte(data), 0600)
		var c setup.Config
		if err := readInstallConfig(file, &c); err == nil {
			t.Fatal("accepted ambiguous or secret platform configuration")
		}
	}
}

func TestInstallProfileIsPrivateAndNeverOverwritesAnotherCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	file, err := saveInstallProfile("test", "https://kuberpack.example.com", "kp_first")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("profile permissions must be 0600")
	}
	if _, err := saveInstallProfile("test", "https://kuberpack.example.com", "kp_first"); err != nil {
		t.Fatal(err)
	}
	if _, err := saveInstallProfile("test", "https://kuberpack.example.com", "kp_other"); err == nil {
		t.Fatal("overwrote another credential")
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "kp_first") {
		t.Fatal("original credential lost")
	}
}

func TestBootstrapCredentialsArePreservedWhenTheAPIWasGenerated(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuberpack", Namespace: "test"}, Data: map[string][]byte{"KUBERPACK_API_TOKEN": []byte("kp_preserved")}})
	i := &installation{c: setup.Config{Namespace: "test", BuildNamespace: "build", RegistryURL: "ghcr.io", RegistryUser: "test", GitUser: "test"}, kube: client, credentials: installCredentials{Git: "read-token", Registry: "push-token"}}
	if err := i.prepareCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	if i.credentials.API != "kp_preserved" {
		t.Fatal("API token rotated")
	}
	i.credentials.API = "different"
	if err := i.prepareCredentials(ctx); err == nil || !strings.Contains(err.Error(), "rotation") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestPrivatePlatformImagesHaveRegistryCredentialsBeforeTheirFirstPull(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	i := &installation{
		c:    setup.Config{Namespace: "test", BuildNamespace: "build", RegistryURL: "https://ghcr.io", RegistryUser: "test", GitUser: "test"},
		kube: client, credentials: installCredentials{Git: "git-only-token", Registry: "registry-only-token"},
	}
	if err := i.prepareCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	values, err := i.platformValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pulls, ok := values["imagePullSecrets"].([]any)
	if !ok || len(pulls) != 1 {
		t.Fatal("control plane has no image pull credential")
	}
	name := pulls[0].(map[string]any)["name"].(string)
	for _, target := range []struct{ namespace, name string }{{"test", name}, {"build", "kuberpack-builder-pull"}} {
		secret, err := client.CoreV1().Secrets(target.namespace).Get(ctx, target.name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("image pull credential missing in %s: %v", target.namespace, err)
		}
		var config struct {
			Auths map[string]struct{ Username, Password string }
		}
		if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config); err != nil {
			t.Fatal(err)
		}
		credential, ok := config.Auths["ghcr.io"]
		if secret.Type != corev1.SecretTypeDockerConfigJson || !ok || credential.Username != "test" || credential.Password != i.credentials.Registry {
			t.Fatalf("invalid image pull credential in %s", target.namespace)
		}
	}
}

func TestInstallerGitOpsIsStableAfterFluxLabelsItsOwnedIssuer(t *testing.T) {
	ctx := context.Background()
	issuer := object("cert-manager.io/v1", "ClusterIssuer", "", "test-issuer", map[string]any{"acme": map[string]any{"server": "https://acme.example.org/directory"}})
	issuer.SetLabels(map[string]string{installOwner: "test"})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), issuer)
	i := &installation{c: setup.Config{Cluster: "test", TLS: true, Issuer: "test-issuer"}, dyn: dyn}
	before, err := i.durableInfrastructure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.SetLabels(map[string]string{installOwner: "test", "kustomize.toolkit.fluxcd.io/name": "kuberpack-platform", "kustomize.toolkit.fluxcd.io/namespace": "kuberpack"})
	if _, err := dyn.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}).Update(ctx, issuer, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := i.durableInfrastructure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Flux inventory labels changed the generated installation files")
	}
}

func TestInstallerPassesTheBuildPlacementToTheChart(t *testing.T) {
	ctx := context.Background()
	base := setup.Config{
		Cluster: "demo", Namespace: "kuberpack", BuildNamespace: "kuberpack-build",
		GitProvider: "forgejo", GitURL: "https://git.example.test", GitOpsURL: "https://git.example.test/team/platform.git",
		GitOpsBranch: "main", AppsURL: "https://git.example.test/team/applications.git", AppsBranch: "main", Root: "clusters/demo/kuberpack", Source: "kuberpack-gitops",
		Image: "example.test/kuberpack:test", BuilderImage: "example.test/builder:test",
	}
	i := &installation{c: base, kube: fake.NewSimpleClientset()}
	values, err := i.platformValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := values["builderPlacement"]; found {
		t.Fatal("le placement est écrit alors qu'aucune valeur n'est fournie")
	}
	i.c.BuilderRuntimeClass = "gvisor"
	i.c.BuilderNodeSelector = `{"kuberpack.dev/build":"true"}`
	i.c.BuilderTolerations = `[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]`
	values, err = i.platformValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	placement, ok := values["builderPlacement"].(map[string]any)
	if !ok {
		t.Fatalf("placement absent des valeurs du chart: %#v", values["builderPlacement"])
	}
	if placement["runtimeClass"] != "gvisor" {
		t.Fatalf("RuntimeClass non transmis: %#v", placement)
	}
	selector, ok := placement["nodeSelector"].(map[string]string)
	if !ok || selector["kuberpack.dev/build"] != "true" {
		t.Fatalf("sélecteur non transmis: %#v", placement["nodeSelector"])
	}
	tolerations, ok := placement["tolerations"].([]corev1.Toleration)
	if !ok || len(tolerations) != 1 || tolerations[0].Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("tolérances non transmises: %#v", placement["tolerations"])
	}
}

func TestInstallerWaitsForNewConfigurationEvenWhenTheOldReleaseIsHealthy(t *testing.T) {
	ctx := context.Background()
	release := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "test", "kuberpack", map[string]any{"values": map[string]any{"image": "old-image"}})
	release.SetGeneration(1)
	release.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), release)
	i := &installation{c: setup.Config{Namespace: "test"}, dyn: dyn, controlValues: map[string]any{"image": "new-image"}}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := i.waitControlPlaneValues(bounded); err == nil {
		t.Fatal("an old healthy release satisfied the requested configuration")
	}
	release.Object["spec"].(map[string]any)["values"] = map[string]any{"image": "new-image"}
	if _, err := dyn.Resource(schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}).Namespace("test").Update(ctx, release, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := i.waitControlPlaneValues(ctx); err != nil {
		t.Fatal(err)
	}
}

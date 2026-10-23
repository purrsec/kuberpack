package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestManifestRejectsInputThatWouldFailAfterDeployment(t *testing.T) {
	for name, fields := range map[string]string{
		"two sources":         "repository: owner/app\n    image: example.org/app:1",
		"negative replicas":   "image: example.org/app:1\n    replicas: -1",
		"invalid port":        "image: example.org/app:1\n    port: 65536",
		"short startup":       "image: example.org/app:1\n    startup_budget: 1",
		"invalid memory":      "image: example.org/app:1\n    resources:\n      limits: {memory: nonsense}",
		"request over limit":  "image: example.org/app:1\n    resources:\n      requests: {memory: 2Gi}\n      limits: {memory: 1Gi}",
		"relative volume":     "image: example.org/app:1\n    volumes: [{name: data, path: data, size: 1Gi}]",
		"invalid volume size": "image: example.org/app:1\n    volumes: [{name: data, path: /data, size: -1Gi}]",
		"duplicate volume":    "image: example.org/app:1\n    volumes: [{name: data, path: /data}, {name: data, path: /other}]",
		"ignored build":       "image: example.org/app:1\n    build: {build_command: make}",
		"secret shadowed":     "image: example.org/app:1\n    secrets: [PASSWORD]\n    env: {PASSWORD: public}",
		"extra document":      "image: example.org/app:1\n---\nproject: ignored",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProjectManifest([]byte("project: demo\nservices:\n  app:\n    " + fields + "\n"))
			if err == nil {
				t.Fatal("invalid source was accepted")
			}
		})
	}
}

func TestProjectSecretsAreSelfContainedRequiredAndUpdated(t *testing.T) {
	bare := setupGitOps(t, "")
	m := ProjectManifest{Project: "demo", Services: map[string]Service{
		"app": {Image: "example.org/app@" + testDigest, Hostname: "demo.example.org", Secrets: []string{"PASSWORD"}},
	}}
	req := ProjectRequest{GitOpsURL: bare, Manifest: m, Platform: Platform{InfisicalProjectID: "test-project"}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	path := filepath.Join(clone, "kubernetes/vps/projects/demo")
	rendered, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(filesys.MakeFsOnDisk(), path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rendered.AsYaml()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: InfisicalStaticSecret", "namespace: demo", "namespace: infisical", "secretKeyRef:", "key: PASSWORD"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the project alone must provide %q:\n%s", want, raw)
		}
	}
	if result, err := EnsureProject(context.Background(), req); err != nil || result.Changed {
		t.Fatalf("compiling unchanged source must be idempotent: %+v %v", result, err)
	}
	svc := m.Services["app"]
	svc.Secrets = []string{"NEW_PASSWORD"}
	m.Services["app"] = svc
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	clone = readRemoteTree(t, bare)
	secret := readFile(t, clone, "kubernetes/vps/projects/demo/app/secret.yaml")
	if !strings.Contains(secret, "NEW_PASSWORD") || strings.Contains(secret, `index . "PASSWORD"`) {
		t.Fatalf("secret keys must follow source edits:\n%s", secret)
	}
	svc.Secrets = nil
	m.Services["app"] = svc
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	clone = readRemoteTree(t, bare)
	if _, err := os.Stat(filepath.Join(clone, "kubernetes/vps/projects/demo/app/secret.yaml")); !os.IsNotExist(err) {
		t.Fatal("removing secrets from the source must remove the generated sync")
	}
	values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml")
	if strings.Contains(values, "secretKeyRef:") || strings.Contains(values, "envFrom:") {
		t.Fatal("removed keys must not remain mounted through a legacy envFrom")
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{GitOpsURL: bare, Manifest: ProjectManifest{Project: "missing", Services: map[string]Service{"app": {Repository: "owner/app", Secrets: []string{"PASSWORD"}}}}}); err == nil {
		t.Fatal("declared secrets without Infisical must fail explicitly")
	}
}

func TestHealthTestHonoursTheStartupBudget(t *testing.T) {
	spec := AppSpec{Name: "slow", Hostname: "slow.example.org", Image: "example.org/app@" + testDigest, StartupBudget: 601}
	if !strings.Contains(RenderHelmRelease(spec, Platform{}), "timeout: 661s") {
		t.Fatal("Helm must allow the declared startup budget")
	}
	if strings.Contains(RenderHelmRelease(spec, Platform{}), "disableWait: true") {
		t.Fatal("a health test must not succeed against an old pod while the replacement is unready")
	}
	zero := 0
	paused := spec
	paused.Replicas = &zero
	if !strings.Contains(RenderHelmRelease(paused, Platform{}), "enable: false") {
		t.Fatal("a paused service must not wait for an HTTP health test")
	}
	values := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(values, []byte(RenderValues(spec, Platform{})), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderChart(chartDir(t), spec.Name, values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "activeDeadlineSeconds: 612") || !strings.Contains(rendered, "+ 602 ") {
		t.Fatalf("health test must allow the same startup budget as the probe:\n%s", rendered)
	}
}

func TestCapacityCountsCPURequestsAndDefaultMemory(t *testing.T) {
	manifest := ProjectManifest{Project: "shared", Services: map[string]Service{
		"app": {Repository: "owner/app", Resources: &ServiceResources{
			Requests: ResourceList{CPU: "100m"}, Limits: ResourceList{CPU: "4"},
		}},
	}}
	if err := checkCapacity(manifest, Platform{NodeCPU: 2, NodeMemory: "2Gi"}); err != nil {
		t.Fatalf("CPU burst limits must not be treated as reserved cores: %v", err)
	}
	if got := projectMemoryLimit(manifest); got != 256*1024*1024 {
		t.Fatalf("capacity must include the chart's default memory limit: %v", got)
	}
	manifest.Services["app"] = Service{Repository: "owner/app"}
	if got := projectCPURequest(manifest); got != 0.05 {
		t.Fatalf("capacity must include the chart's default CPU request: %v", got)
	}
}

func TestPublishedServiceWithADiskUsesASingleWriterRollout(t *testing.T) {
	spec := AppSpec{Name: "pdf", Project: "pdf", Namespace: "pdf", Hostname: "pdf.example.org", Image: "example.org/pdf@" + testDigest,
		Volumes: []Volume{{Name: "configs", Path: "/configs", Size: "1Gi"}}}
	values := filepath.Join(t.TempDir(), "values.yaml")
	raw := RenderValues(spec, Platform{})
	if strings.Contains(raw, "imagePullSecrets:") {
		t.Fatal("a public image must not reference an absent platform registry credential")
	}
	if !strings.Contains(RenderHelmRelease(spec, Platform{}), "upgrade:\n    serverSideApply: disabled\n    force: true") {
		t.Fatal("migration from a rolling Deployment must clear its server-side defaults")
	}
	if err := os.WriteFile(values, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderChart(chartDir(t), spec.Name, values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "type: Recreate") || !strings.Contains(rendered, "rollingUpdate: null") {
		t.Fatal("a disk-backed app must stop its old process before starting the new one")
	}
}

func TestRemovingTheLastLegacySecretKeepsTheRootBuildable(t *testing.T) {
	work := t.TempDir()
	p := Platform{InfisicalProjectID: "test-project"}.withDefaults()
	root := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - namespace.yaml\n"
	if err := os.WriteFile(filepath.Join(work, "kustomization.yaml"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "namespace.yaml"), []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := AppSpec{Name: "app", Project: "demo", Namespace: "demo", Secrets: []string{"PASSWORD"}}
	if _, _, err := ensureSecretCR(work, spec, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := removeSecretCR(work, spec, p); err != nil {
		t.Fatal(err)
	}
	if _, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(filesys.MakeFsOnDisk(), work); err != nil {
		t.Fatalf("migrating the last secret must not break platform reconciliation: %v", err)
	}
}

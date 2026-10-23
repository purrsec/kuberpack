package promote

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func scopeManifest(t *testing.T, work string, p Platform, project, namespace string) {
	t.Helper()
	dir := filepath.Join(work, filepath.FromSlash(p.ProjectDir(project)))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw := "project: " + project + "\n"
	if namespace != "" {
		raw += "namespace: " + namespace + "\n"
	}
	raw += "services:\n  app:\n    image: example.org/app:1\n"
	if err := os.WriteFile(filepath.Join(dir, "kuberpack.yaml"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestInfisicalScopeTracksProjectCreationNamespaceAndRemoval(t *testing.T) {
	work := t.TempDir()
	p := Platform{ProjectsDir: "projects", ControlPlaneNamespace: "control", InfisicalOperatorScope: true}
	scopeManifest(t, work, p, "alpha", "runtime-a")
	scopeManifest(t, work, p, "beta", "")
	if err := SyncInfisicalOperatorScope(work, p); err != nil {
		t.Fatal(err)
	}
	assertScope := func(want []string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(work, "projects", InfisicalOperatorScopeFile))
		if err != nil {
			t.Fatal(err)
		}
		var object struct {
			Metadata struct{ Name, Namespace string }
			Data     map[string]string
		}
		if err := yaml.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if object.Metadata.Name != InfisicalOperatorScopeName || object.Metadata.Namespace != "control" {
			t.Fatalf("metadata: %+v", object.Metadata)
		}
		var values struct {
			ScopedRBAC bool     `yaml:"scopedRBAC"`
			Namespaces []string `yaml:"scopedNamespaces"`
		}
		if err := yaml.Unmarshal([]byte(object.Data["values.yaml"]), &values); err != nil {
			t.Fatal(err)
		}
		if !values.ScopedRBAC || !reflect.DeepEqual(values.Namespaces, want) {
			t.Fatalf("scope=%+v want=%v", values, want)
		}
		parent, err := os.ReadFile(filepath.Join(work, "projects", "kustomization.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(parent), "- "+InfisicalOperatorScopeFile) != 1 {
			t.Fatalf("parent: %s", parent)
		}
		return raw
	}
	before := assertScope([]string{"beta", "infisical", "runtime-a"})
	if err := SyncInfisicalOperatorScope(work, p); err != nil {
		t.Fatal(err)
	}
	if after := assertScope([]string{"beta", "infisical", "runtime-a"}); string(before) != string(after) {
		t.Fatal("scope is not deterministic")
	}
	scopeManifest(t, work, p, "gamma", "runtime-a")
	if err := SyncInfisicalOperatorScope(work, p); err != nil {
		t.Fatal(err)
	}
	assertScope([]string{"beta", "infisical", "runtime-a"})
	if err := os.RemoveAll(filepath.Join(work, "projects", "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(work, "projects", "gamma")); err != nil {
		t.Fatal(err)
	}
	if err := SyncInfisicalOperatorScope(work, p); err != nil {
		t.Fatal(err)
	}
	assertScope([]string{"beta", "infisical"})
}

func TestInfisicalScopeRejectsProtectedNamespacesAndRepoSymlinks(t *testing.T) {
	for _, ns := range []string{"kube-system", "kube-public", "kube-node-lease", "default", "flux-system", "kuberpack-build", "control", "infisical"} {
		t.Run(ns, func(t *testing.T) {
			work := t.TempDir()
			p := Platform{ProjectsDir: "projects", ControlPlaneNamespace: "control", InfisicalOperatorScope: true}
			scopeManifest(t, work, p, "app", ns)
			if err := SyncInfisicalOperatorScope(work, p); err == nil {
				t.Fatal("protected namespace accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		work := t.TempDir()
		p := Platform{ProjectsDir: "projects", InfisicalOperatorScope: true}
		if err := os.Symlink(t.TempDir(), filepath.Join(work, "projects")); err != nil {
			t.Fatal(err)
		}
		if err := SyncInfisicalOperatorScope(work, p); err == nil {
			t.Fatal("scope followed a symlink")
		}
	})
	t.Run("invalid manifest", func(t *testing.T) {
		work := t.TempDir()
		p := Platform{ProjectsDir: "projects", InfisicalOperatorScope: true}
		scopeManifest(t, work, p, "app", "")
		if err := os.WriteFile(filepath.Join(work, "projects/app/kuberpack.yaml"), []byte("project: app\nunknown: field\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := SyncInfisicalOperatorScope(work, p); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	})
}

func TestInfisicalScopeIsOptInForExistingExternalOperators(t *testing.T) {
	work := t.TempDir()
	if err := SyncInfisicalOperatorScope(work, Platform{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("external operator scope was changed")
	}
}

func TestProjectCommitsCarryInfisicalScopeChangesAndRemoval(t *testing.T) {
	bare := setupAppsGitOps(t)
	p := Platform{InfisicalOperatorScope: true}
	manifest := func(project, namespace string) ProjectManifest {
		return ProjectManifest{Project: project, Namespace: namespace, Services: map[string]Service{"app": {Image: "example.org/app@" + testDigest, Hostname: project + ".example.org"}}}
	}
	readScope := func() []string {
		t.Helper()
		work := readRemoteTree(t, bare)
		raw := readFile(t, work, filepath.ToSlash(filepath.Join(p.ProjectsDirPath(), InfisicalOperatorScopeFile)))
		var object struct{ Data map[string]string }
		if err := yaml.Unmarshal([]byte(raw), &object); err != nil {
			t.Fatal(err)
		}
		var values struct {
			Namespaces []string `yaml:"scopedNamespaces"`
		}
		if err := yaml.Unmarshal([]byte(object.Data["values.yaml"]), &values); err != nil {
			t.Fatal(err)
		}
		return values.Namespaces
	}
	for _, m := range []ProjectManifest{manifest("alpha", "alpha-runtime"), manifest("beta", "beta-runtime")} {
		if _, err := EnsureProject(context.Background(), ProjectRequest{GitOpsURL: bare, Manifest: m, Platform: p}); err != nil {
			t.Fatal(err)
		}
	}
	if got := readScope(); !reflect.DeepEqual(got, []string{"alpha-runtime", "beta-runtime", "infisical"}) {
		t.Fatalf("scope=%v", got)
	}
	if result, err := EnsureProject(context.Background(), ProjectRequest{GitOpsURL: bare, Manifest: manifest("beta", "beta-runtime"), Platform: p}); err != nil || result.Changed {
		t.Fatalf("recompile changed scope: %+v %v", result, err)
	}
	if _, err := DeleteProject(context.Background(), ProjectRequest{GitOpsURL: bare, Manifest: ProjectManifest{Project: "alpha"}, Platform: p}); err != nil {
		t.Fatal(err)
	}
	if got := readScope(); !reflect.DeepEqual(got, []string{"beta-runtime", "infisical"}) {
		t.Fatalf("removed namespace remains in scope: %v", got)
	}
}

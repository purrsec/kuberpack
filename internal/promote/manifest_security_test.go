package promote

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManifestRejectsGeneratedYAMLInjection(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ProjectManifest)
	}{
		{"healthcheck", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.Healthcheck = "/\nenvFrom:\n  - secretRef: {name: stolen}"
			m.Services["web"] = s
		}},
		{"environment", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.SecretEnv = "prod\n  injected: true"
			m.Services["web"] = s
		}},
		{"capability", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.Capabilities = []string{"CHOWN\nenvFrom: []"}
			m.Services["web"] = s
		}},
		{"volume path", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.Volumes = []Volume{{Name: "data", Path: "/data\nenvFrom: []"}}
			m.Services["web"] = s
		}},
		{"volume claim", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.Volumes = []Volume{{Name: "data", Path: "/data", Claim: "data\nenvFrom: []"}}
			m.Services["web"] = s
		}},
		{"addon database", func(m *ProjectManifest) {
			m.Addons["db"] = Addon{Engine: "cnpg", Database: "app\n  enableSuperuserAccess: true"}
		}},
		{"addon owner", func(m *ProjectManifest) { m.Addons["db"] = Addon{Engine: "cnpg", Owner: "app # ignored"} }},
		{"addon yaml type", func(m *ProjectManifest) { m.Addons["db"] = Addon{Engine: "cnpg", Database: "null"} }},
		{"addon path", func(m *ProjectManifest) { m.Addons["db"] = Addon{Engine: "volume", Path: "/data\nenvFrom: []"} }},
		{"addon retention", func(m *ProjectManifest) {
			m.Addons["db"] = Addon{Engine: "cnpg", Backup: &Backup{Retention: "30d\n  injected: true"}}
		}},
		{"addon quantity", func(m *ProjectManifest) {
			m.Addons["db"] = Addon{Engine: "cnpg", Params: map[string]string{"memory": "1Gi\n  injected: true"}}
		}},
		{"addon instances overflow", func(m *ProjectManifest) {
			m.Addons["db"] = Addon{Engine: "cnpg", Params: map[string]string{"instances": "18446744073709551617"}}
		}},
		{"build directory escape", func(m *ProjectManifest) {
			s := m.Services["web"]
			s.Build = &BuildOptions{RootDirectory: "../../tokens"}
			m.Services["web"] = s
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := ProjectManifest{Project: "demo", Services: map[string]Service{"web": {Repository: "owner/repo"}}, Addons: map[string]Addon{}}
			test.change(&m)
			if err := validateManifest(m); err == nil {
				t.Fatal("unsafe manifest was accepted")
			}
		})
	}
}

func TestManifestRejectsReservedNamespaces(t *testing.T) {
	for _, namespace := range []string{"default", "kube-system", "kube-public", "kube-node-lease", "kuberpack", "kuberpack-build", "infisical", "flux-system", "cert-manager"} {
		t.Run(namespace, func(t *testing.T) {
			if _, err := ParseProjectManifest([]byte("project: demo\nnamespace: " + namespace + "\n")); err == nil {
				t.Fatal("reserved namespace accepted")
			}
		})
	}
	if _, err := ParseProjectManifest([]byte("project: demo\nnamespace: demo-runtime\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeValuesKeepScalarsOutOfGeneratedStructure(t *testing.T) {
	injection := "/\nenvFrom:\n  - secretRef: {name: stolen}"
	spec := AppSpec{Name: "web", Project: "demo", Healthcheck: injection, Capabilities: []string{injection}, Volumes: []Volume{{Name: "data", Path: injection, Claim: injection}}}
	raw := RenderValues(spec, Platform{})
	var value map[string]any
	if err := yaml.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	if value["healthcheck"] != injection || value["envFrom"] != nil {
		t.Fatal("healthcheck introduced YAML structure")
	}
	volume := value["volumes"].([]any)[0].(map[string]any)
	if volume["path"] != injection || volume["claim"] != injection {
		t.Fatal("volume scalar was not preserved")
	}
}

func TestInfisicalEnvironmentRemainsOneScalar(t *testing.T) {
	injection := "prod\n      secretPath: /other\n---\napiVersion: v1\nkind: Secret"
	raw := RenderSecretCR(AppSpec{Name: "web", Project: "demo", Namespace: "demo", Secrets: []string{"PASSWORD"}, SecretEnv: injection}, Platform{InfisicalProjectID: "test"})
	decoder := yaml.NewDecoder(bytes.NewBufferString(raw))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	source := value["spec"].(map[string]any)["sources"].([]any)[0].(map[string]any)
	if source["environmentSlug"] != injection {
		t.Fatal("environment was not preserved as a scalar")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("environment introduced an additional YAML document")
	}
}

func TestReloadRejectsManifestForAnotherDirectory(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "attacker", "project: victim\nservices:\n  web:\n    repository: owner/repo\n")
	_, err := ReloadProjects(context.Background(), ReloadRequest{GitOpsURL: bare, Projects: []string{"attacker"}})
	if err == nil || !strings.Contains(err.Error(), "must match its directory") {
		t.Fatalf("expected directory mismatch refusal, got %v", err)
	}
	tree := readRemoteTree(t, bare)
	if _, err := os.Stat(filepath.Join(tree, "kubernetes/vps/projects/victim")); !os.IsNotExist(err) {
		t.Fatal("another project directory was generated")
	}
}

func TestGeneratedTreesRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(filepath.Join(projects, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "innocent-file"), filepath.Join(projects, "demo", "kuberpack.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := rejectRepoSymlinks(root, "projects"); err == nil {
		t.Fatal("versioned file symlink accepted")
	}
	if err := rejectRepoSymlinks(root, "../outside"); err == nil {
		t.Fatal("escaping path accepted")
	}
	if err := rejectRepoSymlinks(root, "new-projects"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "ancestor")); err != nil {
		t.Fatal(err)
	}
	if err := rejectRepoSymlinks(root, "ancestor/projects"); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
}

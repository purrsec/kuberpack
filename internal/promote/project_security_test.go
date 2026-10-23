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

func TestDirectedNeighboursCompileToExactPortsAndReverseIngress(t *testing.T) {
	m, err := ParseProjectManifest([]byte(`project: isolated
services:
  front:
    repository: owner/front
    hostname: front.example.org
    peers: [back]
  back:
    repository: owner/back
    hostname: back.example.org
    port: 9090
    peers: []
  other:
    repository: owner/other
    hostname: other.example.org
    peers: []
policies:
  peers: [other]
`))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	p := Platform{ProjectsDir: "projects"}
	if _, _, _, err := compileProjectFiles(context.Background(), work, m, p, "", true); err != nil {
		t.Fatal(err)
	}
	readValues := func(service string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(work, "projects/isolated", service, "values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		return values["networkPolicy"].(map[string]any)
	}
	front, back, other := readValues("front"), readValues("back"), readValues("other")
	if front["internet"] != false || front["strictPeers"] != true || front["project"] != "isolated" {
		t.Fatalf("network defaults=%v", front)
	}
	if !reflect.DeepEqual(front["servicePeers"], []any{map[string]any{"name": "back", "port": 9090}}) {
		t.Fatalf("outgoing=%v", front)
	}
	if !reflect.DeepEqual(back["incomingPeers"], []any{"front"}) {
		t.Fatalf("incoming=%v", back)
	}
	if len(back["servicePeers"].([]any)) != 0 || len(other["incomingPeers"].([]any)) != 0 {
		t.Fatal("an empty service allowlist inherited project peers")
	}
	// Empty lists must survive saving and reopening a source manifest.
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := ParseProjectManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Services["back"].Peers == nil || len(servicePeerNames(reopened, reopened.Services["back"])) != 0 {
		t.Fatal("explicit [] disappeared on manifest round trip")
	}
}

func TestProjectNamespaceAndGlobalIdentityCannotBeClaimedByAnotherProject(t *testing.T) {
	work := t.TempDir()
	p := Platform{ProjectsDir: "projects", ControlPlaneNamespace: "custom-control", BuildNamespace: "custom-builder"}.withDefaults()
	scopeManifest(t, work, p, "alpha", "runtime-alpha")
	for name, m := range map[string]ProjectManifest{
		"shared namespace":    {Project: "beta", Namespace: "runtime-alpha"},
		"namespace migration": {Project: "alpha", Namespace: "runtime-beta"},
		"control plane":       {Project: "beta", Namespace: "custom-control"},
		"builder":             {Project: "beta", Namespace: "custom-builder"},
		"system":              {Project: "beta", Namespace: "kube-system"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateProjectNamespace(work, m, p); err == nil {
				t.Fatal("namespace ownership escaped")
			}
		})
	}
	scopeManifest(t, work, p, "a-b", "")
	if err := os.WriteFile(filepath.Join(work, "projects/a-b/kuberpack.yaml"), []byte("project: a-b\nservices:\n  c:\n    repository: owner/c\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectNamespace(work, ProjectManifest{Project: "a", Services: map[string]Service{"b-c": {Repository: "owner/c"}}}, p); err == nil {
		t.Fatal("a-b/c and a/b-c claimed the same application identity")
	}
}

func TestManifestCannotReachAnUndeclaredOrCrossProjectNeighbour(t *testing.T) {
	for _, peer := range []string{"other-project/back", "missing", "back.other-project.svc.cluster.local"} {
		_, err := ParseProjectManifest([]byte("project: isolated\nservices:\n  front:\n    repository: owner/front\n    peers: [" + peer + "]\n"))
		if err == nil {
			t.Fatalf("undeclared peer %q accepted", peer)
		}
	}
}

func TestAddonIngressUsesDeclaredConsumersAndBackupDependencies(t *testing.T) {
	m := ProjectManifest{Project: "demo", Services: map[string]Service{
		"allowed": {Addons: []string{"cache"}}, "other": {},
	}, Addons: map[string]Addon{"db": {Backup: &Backup{S3: "storage"}}, "cache": {}, "storage": {}}}
	clients := addonServiceClients(m, "cache")
	if !reflect.DeepEqual(clients, []string{"allowed"}) {
		t.Fatal(clients)
	}
	policy := addonNetworkPolicy("cache", "demo", "valkey", "cache", "demo", []int{6379},
		addonEgress{ReplicaPorts: []int{6379}}, AddonSpec{ServiceClients: clients})
	if !strings.Contains(policy, "app.kubernetes.io/name: allowed") || strings.Contains(policy, "app.kubernetes.io/name: other") {
		t.Fatal(policy)
	}
	if !reflect.DeepEqual(addonDependencyClients(m, "storage"), []string{"db"}) {
		t.Fatal("backup dependency missing")
	}
}

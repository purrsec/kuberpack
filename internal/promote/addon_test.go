package promote

import (
	"context"
	"strings"
	"testing"
)

// A service that uses an addon gets its environment and may reach it. The
// value of the database stays in the Secret CloudNativePG generates.
func TestServiceUsesTheEnvOfItsAddon(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "site.example.org",
				Addons:     []string{"postgres"},
			},
		},
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "site"},
		},
		Policies: Policy{DefaultDeny: BoolPtr(true)},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")

	if !strings.Contains(values, "name: DATABASE_URL") {
		t.Fatalf("the addon must give its environment:\n%s", values)
	}
	if !strings.Contains(values, "name: demo-postgres-app") {
		t.Fatalf("the value must come from the secret of the addon:\n%s", values)
	}
	if !strings.Contains(values, "key: uri") {
		t.Fatalf("the value must come from the uri of the addon:\n%s", values)
	}
	// The service may reach its addon.
	if !strings.Contains(values, "kind: postgres") || !strings.Contains(values, "port: 5432") {
		t.Fatalf("the addon must be a network peer:\n%s", values)
	}
}

// An addon that a service uses and that the policy names is a single peer.
func TestAddonPeerIsNotDuplicated(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "site.example.org",
				Addons:     []string{"postgres"},
			},
		},
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg"},
		},
		Policies: Policy{DefaultDeny: BoolPtr(true), Peers: []string{"postgres"}},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if strings.Count(values, "kind: postgres") != 1 {
		t.Fatalf("the addon peer must appear once:\n%s", values)
	}
}

// The engine of an addon is pluggable: a new type is a new engine.
func TestValkeyAddonRendersItsOwnObjects(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "site.example.org",
				Addons:     []string{"cache"},
			},
		},
		Addons: map[string]Addon{
			"cache": {Engine: "valkey"},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	stateful := readFile(t, clone, "kubernetes/vps/projects/demo/addons/cache/valkey.yaml")
	if !strings.Contains(stateful, "kind: StatefulSet") {
		t.Fatalf("valkey:\n%s", stateful)
	}
	if !strings.Contains(stateful, "kuberpack.io/addon: valkey") {
		t.Fatalf("the addon must be labelled:\n%s", stateful)
	}
	service := readFile(t, clone, "kubernetes/vps/projects/demo/addons/cache/service.yaml")
	if !strings.Contains(service, "kind: Service") || !strings.Contains(service, "port: 6379") {
		t.Fatalf("service:\n%s", service)
	}
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "name: VALKEY_URL") {
		t.Fatalf("the addon must give its environment:\n%s", values)
	}
	if !strings.Contains(values, "valkey://demo-cache:6379") {
		t.Fatalf("the url of the cache:\n%s", values)
	}
	if !strings.Contains(values, "kind: valkey") || !strings.Contains(values, "port: 6379") {
		t.Fatalf("the addon must be a network peer:\n%s", values)
	}
}

// An unknown engine is refused, and the error says what is available.
func TestUnknownAddonEngineIsRefused(t *testing.T) {
	_, err := ParseProjectManifest([]byte(`project: demo
addons:
  cache:
    engine: memcached
`))
	if err == nil {
		t.Fatal("an unknown engine must be refused")
	}
	if !strings.Contains(err.Error(), "cnpg") || !strings.Contains(err.Error(), "valkey") {
		t.Fatalf("the error must list the available engines: %v", err)
	}
}

// A service that uses an addon the project does not declare is refused.
func TestServiceWithAnUnknownAddonIsRefused(t *testing.T) {
	_, err := ParseProjectManifest([]byte(`project: demo
services:
  site:
    repository: owner/repo
    addons:
      - postgres
`))
	if err == nil {
		t.Fatal("an unknown addon must be refused")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("the error must name the addon: %v", err)
	}
}

// The engines are registered in one place: adding a type of addon is adding
// an engine.
func TestAddonEnginesAreRegistered(t *testing.T) {
	names := AddonEngineNames()
	if len(names) < 2 {
		t.Fatalf("engines: %v", names)
	}
	for _, name := range names {
		if _, ok := AddonEngineFor(name); !ok {
			t.Fatalf("the engine %q must be available", name)
		}
	}
	if _, ok := AddonEngineFor("nope"); ok {
		t.Fatal("an unknown engine must not be available")
	}
}

// Two databases of the same project must not be indistinguishable: a service
// that declares one reaches the other.
func TestTheNetworkPeerCarriesTheNameOfTheAddon(t *testing.T) {
	peers := []AddonPeer{
		{Kind: "postgres", Name: "reporting", For: "boutique", Port: 5432},
		{Kind: "postgres", Name: "orders", For: "boutique", Port: 5432},
	}
	values := valuesYAML(AppSpec{
		Name:        "orders-api",
		Hostname:    "orders.151.115.39.23.nip.io",
		Port:        8080,
		Healthcheck: "/healthz",
		AddonPeers:  peers,
	}, Platform{AddonOperators: []string{"cnpg"}})
	for _, want := range []string{"kind: postgres", "name: reporting", "name: orders", "for: boutique", "port: 5432"} {
		if !strings.Contains(values, want) {
			t.Errorf("the values do not carry %q:\n%s", want, values)
		}
	}
}

// The peers of the same kind are not merged when they are not the same addon.
func TestThePeersOfTwoDatabasesAreNotMerged(t *testing.T) {
	merged := mergeAddonPeers(
		[]AddonPeer{{Kind: "postgres", Name: "reporting", For: "boutique", Port: 5432}},
		[]AddonPeer{{Kind: "postgres", Name: "orders", For: "boutique", Port: 5432}},
	)
	if len(merged) != 2 {
		t.Fatalf("merged %d peers, want 2", len(merged))
	}
}

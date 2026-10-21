package promote

import (
	"context"
	"strings"
	"testing"
)

// The catalogue holds the sizes of the addons. The manifest chooses one and
// overrides what it needs.
func TestTheCatalogueHoldsThePlans(t *testing.T) {
	entry, ok := AddonCatalogEntry("postgres")
	if !ok {
		t.Fatal("the type postgres is not in the catalogue")
	}
	if entry.Port != 5432 {
		t.Errorf("the port is %d, want 5432", entry.Port)
	}
	if !strings.Contains(entry.Image, "@sha256:") {
		t.Errorf("the image %q of the catalogue is not pinned", entry.Image)
	}
	plan, err := entry.Plan("medium", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Instances != 2 || plan.Storage != "10Gi" {
		t.Errorf("the medium plan is %+v", plan)
	}
	// The default is the small plan.
	plan, err = entry.Plan("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Instances != 1 {
		t.Errorf("the default plan is %+v", plan)
	}
	// The parameters override the plan.
	plan, err = entry.Plan("small", map[string]string{"instances": "3", "storage": "20Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Instances != 3 || plan.Storage != "20Gi" {
		t.Errorf("the overridden plan is %+v", plan)
	}
	// A plan that does not exist is refused.
	if _, err := entry.Plan("enormous", nil); err == nil {
		t.Error("an unknown plan must be refused")
	}
}

// The catalogue is the source of the contract: the variables an application
// receives.
func TestTheCatalogueHoldsTheContract(t *testing.T) {
	entry, ok := AddonCatalogEntry("postgres")
	if !ok {
		t.Fatal("the type postgres is not in the catalogue")
	}
	if entry.Env["DATABASE_URL"] != "uri" {
		t.Errorf("the contract is %v", entry.Env)
	}
	if len(entry.Env) < 2 {
		t.Errorf("the contract is too small: %v", entry.Env)
	}
}

// A type that writes the resources of an operator is refused when the
// platform does not declare it: the whole project would fail to apply.
func TestATypeThatNeedsAnOperatorIsRefusedWithoutIt(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project:  "demo",
		Addons:   map[string]Addon{"postgres": {Engine: "cnpg", Database: "demo"}},
		Services: map[string]Service{"app": {Type: "stateless", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{}}
	_, err := EnsureProject(context.Background(), req)
	if err == nil {
		t.Fatal("a type that needs an operator must be refused when the platform does not declare it")
	}
	if !strings.Contains(err.Error(), "cnpg") {
		t.Errorf("the error does not name the operator: %v", err)
	}
	// The platform that declares the operator compiles the project.
	req.Platform = Platform{AddonOperators: []string{"cnpg"}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatalf("the platform that declares the operator must compile the project: %v", err)
	}
}

// The pods of an addon are reachable by the applications of their project,
// and by nothing else.
func TestTheAddonPodsAreIsolated(t *testing.T) {
	entry, ok := AddonCatalogEntry("postgres")
	if !ok {
		t.Fatal("the type postgres is not in the catalogue")
	}
	policy := addonNetworkPolicy("boutique-postgres", "boutique", entry.Kind, "postgres", "boutique",
		[]int{5432}, "cnpg-system")
	for _, want := range []string{
		"kind: NetworkPolicy",
		"kuberpack.io/addon: postgres",
		"kuberpack.io/addon-name: postgres",
		"port: 5432",
		"kubernetes.io/metadata.name: cnpg-system",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not carry %q:\n%s", want, policy)
		}
	}
}

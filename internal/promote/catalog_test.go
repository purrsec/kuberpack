package promote

import (
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

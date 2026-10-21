package promote

import (
	"context"
	"gopkg.in/yaml.v3"
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

// The object storage of the platform: its credentials are born in the
// cluster, by a Job, and never in the repository.
func TestTheObjectStorageHasItsCredentialsBornInTheCluster(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Addons: map[string]Addon{
			"rustfs":   {Engine: "rustfs", Plan: "small"},
			"postgres": {Engine: "cnpg", Database: "demo", Backup: &Backup{S3: "rustfs", Retention: "30d"}},
		},
		Services: map[string]Service{"app": {Type: "stateless", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{AddonOperators: []string{"cnpg"}}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tree := readRemoteTree(t, bare)

	// The Job that creates the credentials is in the repository, the
	// credentials are not.
	job := readFile(t, tree, "kubernetes/vps/projects/demo/addons/rustfs/bootstrap-job.yaml")
	for _, want := range []string{"kind: Job", "stringData", "accessKeyId", "secretAccessKey"} {
		if !strings.Contains(job, want) {
			t.Errorf("the bootstrap job does not carry %q:\n%s", want, job)
		}
	}
	// The Role that lets it write the secret is in the repository too.
	rbac := readFile(t, tree, "kubernetes/vps/projects/demo/addons/rustfs/rbac.yaml")
	if !strings.Contains(rbac, "kind: Role") || !strings.Contains(rbac, "resourceNames: [demo-rustfs-app]") {
		t.Errorf("the role of the bootstrap is wrong:\n%s", rbac)
	}

	// The database is backed up to the storage, with references.
	db := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/database.yaml")
	for _, want := range []string{"barmanObjectStore", "demo-rustfs-app", "retentionPolicy: 30d"} {
		if !strings.Contains(db, want) {
			t.Errorf("the backup does not carry %q:\n%s", want, db)
		}
	}
}

// A backup that goes nowhere is refused: the project must declare the
// storage it uses.
func TestABackupWithoutStorageIsRefused(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "demo", Backup: &Backup{S3: "nowhere"}},
		},
		Services: map[string]Service{"app": {Type: "stateless", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{AddonOperators: []string{"cnpg"}}}
	if _, err := EnsureProject(context.Background(), req); err == nil {
		t.Fatal("a backup that goes nowhere must be refused")
	}
}

// A third party application is deployed by running the image its authors
// publish: the platform does not build it, and it never writes in their
// repository.
func TestAThirdPartyApplicationRunsAPublishedImage(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"stirling": {Type: "stateless", Image: "docker.io/stirlingtools/stirling-pdf@sha256:10164ffe", Port: 8080, Hostname: "stirling.host.bzh"},
		},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true}}
	res, err := EnsureProject(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 {
		t.Fatalf("the services are %v", res.Services)
	}
	tree := readRemoteTree(t, bare)
	values := readFile(t, tree, "kubernetes/vps/projects/demo/stirling/values.yaml")
	if !strings.Contains(values, "stirlingtools/stirling-pdf@sha256:10164ffe") {
		t.Errorf("the values do not carry the image:\n%s", values)
	}
	// The service is activated: it runs an image that is pinned.
	parent := readFile(t, tree, "kubernetes/vps/projects/demo/kustomization.yaml")
	if !strings.Contains(parent, "stirling") {
		t.Errorf("the service is not activated:\n%s", parent)
	}
}

// The values of a service must stay parsable, whatever the service
// declares.
func TestTheValuesOfAServiceWithVolumesStayParsable(t *testing.T) {
	values := valuesYAML(AppSpec{
		Name:         "stirling",
		Hostname:     "stirling.host.bzh",
		Port:         8080,
		Healthcheck:  "/",
		Image:        "docker.io/stirlingtools/stirling-pdf@sha256:10164ffe",
		Volumes:      []Volume{{Name: "toolbox", Path: "/usr/local/bin", Size: "1Gi"}},
		WritableRoot: true,
		RunAsRoot:    true,
		AddonPeers:   []AddonPeer{{Kind: "postgres", Name: "main", For: "demo", Port: 5432}},
	}, Platform{NetworkPolicy: true})
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(values), &doc); err != nil {
		t.Fatalf("the values are not parsable: %v\n%s", err, values)
	}
	if doc["writableRoot"] != true || doc["runAsRoot"] != true {
		t.Errorf("the root of the application is not declared:\n%s", values)
	}
	volumes, ok := doc["volumes"].([]any)
	if !ok || len(volumes) != 1 {
		t.Errorf("the volumes are not declared:\n%s", values)
	}
	if _, ok := doc["networkPolicy"].(map[string]any); !ok {
		t.Errorf("the network policy is not declared:\n%s", values)
	}
}

// The document database: its credentials are born in the cluster, and the
// application finds them in the secret.
func TestTheDocumentDatabaseHasItsCredentialsBornInTheCluster(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Addons:  map[string]Addon{"mongo": {Engine: "mongo", Plan: "small"}},
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh"},
		},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true}}
	res, err := EnsureProject(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	tree := readRemoteTree(t, bare)
	job := readFile(t, tree, "kubernetes/vps/projects/demo/addons/mongo/bootstrap-job.yaml")
	if !strings.Contains(job, "kind: Job") || !strings.Contains(job, "username") {
		t.Errorf("the bootstrap job is wrong:\n%s", job)
	}
	// The secret is never in the repository.
	values := readFile(t, tree, "kubernetes/vps/projects/demo/app/values.yaml")
	if strings.Contains(values, "password") {
		t.Errorf("the credentials are in the values:\n%s", values)
	}
	// The service receives the contract of the addon.
	peers := res.Services
	if len(peers) != 1 {
		t.Fatalf("the services are %v", peers)
	}
}

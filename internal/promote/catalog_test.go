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

	// The database is backed up to the storage, through the plugin: since
	// CloudNativePG 1.27, the operator refuses the in-tree configuration
	// and a plugin at the same time.
	db := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/database.yaml")
	for _, want := range []string{
		"barman-cloud.cloudnative-pg.io",
		"isWALArchiver: true",
		"barmanObjectName: demo-postgres-store",
	} {
		if !strings.Contains(db, want) {
			t.Errorf("the backup does not carry %q:\n%s", want, db)
		}
	}
	if strings.Contains(db, "barmanObjectStore") {
		t.Errorf("the in-tree configuration must not be there any more:\n%s", db)
	}
	// The destination is a resource of its own, and the credentials are
	// references.
	store := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/objectstore.yaml")
	for _, want := range []string{
		"kind: ObjectStore",
		"retentionPolicy: 30d",
		"demo-rustfs-app",
		"destinationPath: s3://postgres/",
	} {
		if !strings.Contains(store, want) {
			t.Errorf("the object store does not carry %q:\n%s", want, store)
		}
	}
	// The bucket of the backups must exist before the first backup: a Job
	// creates it, with the credentials of the storage.
	bucketJob := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/bucket-job.yaml")
	for _, want := range []string{
		"kind: Job",
		"create_bucket",
		"demo-rustfs-app",
		"kuberpack.io/for: demo",
	} {
		if !strings.Contains(bucketJob, want) {
			t.Errorf("the bucket job does not carry %q:\n%s", want, bucketJob)
		}
	}
	// The scheduled backup goes through the plugin too.
	scheduled := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/scheduledbackup.yaml")
	if !strings.Contains(scheduled, "method: plugin") {
		t.Errorf("the scheduled backup does not go through the plugin:\n%s", scheduled)
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

// A project that does not fit in the node is refused: the cluster would
// start killing the applications that work.
func TestAProjectThatDoesNotFitIsRefused(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh",
				Resources: &ServiceResources{Limits: ResourceList{Memory: "2Gi"}}},
		},
	}
	// The node is big enough: the project compiles.
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NodeMemory: "4Gi"}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatalf("the project fits in the node: %v", err)
	}
	// The node is too small: the project is refused.
	req.Platform = Platform{NodeMemory: "1Gi"}
	if _, err := EnsureProject(context.Background(), req); err == nil {
		t.Fatal("a project that does not fit in the node must be refused")
	} else if !strings.Contains(err.Error(), "2.0Gi") {
		t.Errorf("the error does not say how much is needed: %v", err)
	}
}

// The whole platform must fit in the node: it is the total that kills a
// cluster, not one project.
func TestTheClusterThatDoesNotFitIsRefused(t *testing.T) {
	bare := setupAppsGitOps(t)
	big := ProjectManifest{
		Project: "big",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "big.host.bzh",
				Resources: &ServiceResources{Limits: ResourceList{Memory: "2Gi"}}},
		},
	}
	other := ProjectManifest{
		Project: "other",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "other.host.bzh",
				Resources: &ServiceResources{Limits: ResourceList{Memory: "2Gi"}}},
		},
	}
	platform := Platform{NetworkPolicy: true, NodeMemory: "3Gi"}
	for _, manifest := range []ProjectManifest{big, other} {
		req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: manifest, Platform: platform}
		if _, err := EnsureProject(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	// The two projects fit one after the other, and together they do not.
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: big, Platform: platform,
	}); err != nil {
		t.Fatalf("a project that fits alone must compile: %v", err)
	}
}

// The reload refuses a new project that does not fit with the projects
// that are already deployed.
func TestANewProjectThatDoesNotFitIsRefused(t *testing.T) {
	bare := setupAppsGitOps(t)
	platform := Platform{NetworkPolicy: true, NodeMemory: "3Gi"}
	big := ProjectManifest{
		Project: "big",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "big.host.bzh",
				Resources: &ServiceResources{Limits: ResourceList{Memory: "2Gi"}}},
		},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: big, Platform: platform}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// A second project of the same size does not fit next to the first
	// one: the node has 3 Gi, the two ask for 4 Gi.
	other := ProjectManifest{
		Project: "other",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "other.host.bzh",
				Resources: &ServiceResources{Limits: ResourceList{Memory: "2Gi"}}},
		},
	}
	// The project that is already deployed stays; the new one is refused.
	clone := readRemoteTree(t, bare)
	err := checkClusterAddition(clone, platform, other)
	if err == nil {
		t.Fatal("a new project that does not fit with the deployed ones must be refused")
	}
	if !strings.Contains(err.Error(), "2.0Gi") || !strings.Contains(err.Error(), "3.0Gi") {
		t.Errorf("the refusal does not say how much is needed: %v", err)
	}
	// The project that is already deployed is not refused: it runs.
	err = checkClusterAddition(clone, platform, big)
	if err != nil {
		t.Errorf("a project that is already deployed must not be refused: %v", err)
	}
}

// A service that declares zero replicas is declared and not run: it is
// not the same as a service that says nothing.
func TestAServiceWithZeroReplicasIsDeclaredAndNotRun(t *testing.T) {
	bare := setupAppsGitOps(t)
	zero := 0
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh", Replicas: &zero},
		},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tree := readRemoteTree(t, bare)
	values := readFile(t, tree, "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(values, "replicas: 0") {
		t.Errorf("the service must be declared with zero replicas:\n%s", values)
	}
	// A service that says nothing gets one.
	one := ProjectManifest{
		Project: "other",
		Services: map[string]Service{
			"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "other.host.bzh"},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: one, Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	values = readFile(t, readRemoteTree(t, bare), "kubernetes/vps/projects/other/app/values.yaml")
	if !strings.Contains(values, "replicas: 1") {
		t.Errorf("a service that says nothing must get one replica:\n%s", values)
	}
}

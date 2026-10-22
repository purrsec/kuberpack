package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureProjectCompilesServicesAddonsPolicies(t *testing.T) {
	bare := setupAppsGitOps(t)
	net := true
	m := ProjectManifest{
		Project:   "mitame",
		Namespace: "mitame",
		Services: map[string]Service{
			"site": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "mitame.host.bzh"},
		},
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "umami", Owner: "umami"},
		},
		Policies: Policy{DefaultDeny: BoolPtr(true), Internet: &net, Peers: []string{"postgres"}},
	}
	res, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, IngressTLS: true, AddonOperators: []string{"cnpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("expected a commit")
	}
	clone := readRemoteTree(t, bare)

	// Human source preserved verbatim.
	manifest := readFile(t, clone, "kubernetes/vps/projects/mitame/kuberpack.yaml")
	if !strings.Contains(manifest, "project: mitame") {
		t.Fatalf("manifest:\n%s", manifest)
	}

	// Service compiled to the stateless contract.
	values := readFile(t, clone, "kubernetes/vps/projects/mitame/site/values.yaml")
	if !strings.Contains(values, "hostname: mitame.host.bzh") {
		t.Fatalf("service values:\n%s", values)
	}
	// The addon is a network peer of the services, with its own port.
	if !strings.Contains(values, "kind: postgres") || !strings.Contains(values, "for: mitame") {
		t.Fatalf("expected the addon peer in the service:\n%s", values)
	}
	if !strings.Contains(values, "port: 5432") {
		t.Fatalf("expected the port of the addon peer:\n%s", values)
	}
	if !strings.Contains(values, `image: ""`) {
		t.Fatalf("service must start unpinned:\n%s", values)
	}

	// Addon compiled to a CNPG cluster with the expected labels.
	db := readFile(t, clone, "kubernetes/vps/projects/mitame/addons/postgres/database.yaml")
	if !strings.Contains(db, "kind: Cluster") || !strings.Contains(db, "database: umami") {
		t.Fatalf("addon db:\n%s", db)
	}
	if !strings.Contains(db, "kuberpack.io/for: mitame") {
		t.Fatalf("addon labels:\n%s", db)
	}

	// Parent carries the namespace and lists the addon. The service is not
	// listed yet: its image is not pinned, and Flux must not see an app
	// before the first digest pin.
	parent := readFile(t, clone, "kubernetes/vps/projects/mitame/kustomization.yaml")
	if !strings.Contains(parent, "namespace: mitame") {
		t.Fatalf("parent namespace:\n%s", parent)
	}
	if listedInKustomization([]byte(parent), "site") {
		t.Fatalf("an unpinned service must not be activated:\n%s", parent)
	}
	// The addons have their own directory: a service named like an addon
	// can no longer write over it.
	if !listedInKustomization([]byte(parent), "addons/postgres") {
		t.Fatalf("parent resources:\n%s", parent)
	}

	// The projects parent lists the project, otherwise Flux never sees it.
	projects := readFile(t, clone, "kubernetes/vps/projects/kustomization.yaml")
	if !listedInKustomization([]byte(projects), "mitame") {
		t.Fatalf("projects parent:\n%s", projects)
	}
}

// Deleting a project must drop it from the projects parent, otherwise Flux
// keeps a dangling resource. Deleting an unknown project is a no-op.
func TestDeleteProjectRemovesItFromTheProjectsParent(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project:  "demo",
		Services: map[string]Service{"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tree := readRemoteTree(t, bare)
	parent := readFile(t, tree, "kubernetes/vps/projects/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "demo") {
		t.Fatalf("projects parent:\n%s", parent)
	}

	if _, err := DeleteProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tree = readRemoteTree(t, bare)
	parent = readFile(t, tree, "kubernetes/vps/projects/kustomization.yaml")
	if listedInKustomization([]byte(parent), "demo") {
		t.Fatalf("project is still listed:\n%s", parent)
	}
	if _, err := os.Stat(filepath.Join(tree, "kubernetes", "vps", "projects", "demo")); !os.IsNotExist(err) {
		t.Fatalf("project directory still present: %v", err)
	}

	if _, err := DeleteProject(context.Background(), req); err != nil {
		t.Fatalf("deleting an unknown project must be a no-op: %v", err)
	}
}

func TestEnsureProjectPreservesPinnedImage(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project:  "demo",
		Services: map[string]Service{"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Simulate promote pinning an image by editing values on the remote.
	clone := readRemoteTree(t, bare)
	valuesPath := filepath.Join(clone, "kubernetes", "vps", "projects", "demo", "app", "values.yaml")
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), `image: ""`, `image: "reg/app:sha-abc@sha256:dead"`, 1)
	if err := os.WriteFile(valuesPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, clone, "add", "-A")
	gitT(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "pin")
	gitT(t, clone, "push", "origin", "HEAD:main")

	// Recompiling must keep the pinned image.
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tree := readRemoteTree(t, bare)
	again := readFile(t, tree, "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(again, "sha256:dead") {
		t.Fatalf("pinned image was overwritten:\n%s", again)
	}

	// A pinned service is activated: the parent lists it.
	parent := readFile(t, tree, "kubernetes/vps/projects/demo/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "app") {
		t.Fatalf("a pinned service must be activated:\n%s", parent)
	}
}

// A project that holds a database is not removed silently: the data leaves
// the cluster first.
func TestDeleteProjectRefusesToDropADatabase(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "demo", Owner: "demo"},
		},
		Services: map[string]Service{"app": {Type: "stateless", Repository: "demo/app", Port: 8080, Hostname: "demo.host.bzh"}},
	}
	req := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: m, Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}}}
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	_, err := DeleteProject(context.Background(), req)
	if err == nil {
		t.Fatal("the removal of a project that holds a database must be refused")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("the error does not name the addon: %v", err)
	}

	// The dump of the database is in the repository: the data can leave the
	// cluster before the project does.
	tree := readRemoteTree(t, bare)
	dump := readFile(t, tree, "kubernetes/vps/projects/demo/addons/postgres/dump.yaml")
	if !strings.Contains(dump, "pg_dump") {
		t.Errorf("the dump job is missing:\n%s", dump)
	}

	// The removal can be forced: the data goes with it.
	forced := req
	forced.Force = true
	if _, err := DeleteProject(context.Background(), forced); err != nil {
		t.Fatalf("the forced removal must work: %v", err)
	}
}

// The image of a published application belongs to the manifest: the
// compiler resolves it and pins it. The image of a built application
// belongs to the control plane.
func TestTheImageOfAPublishedApplicationIsPinnedByTheCompiler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	spec := AppSpec{Name: "app", Port: 8080, Image: "docker.io/vaultwarden/server@sha256:abc"}
	p := Platform{}
	if err := writeServiceValues(path, spec, p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "docker.io/vaultwarden/server@sha256:abc") {
		t.Fatalf("the image of the application is not pinned with its repository:\n%s", raw)
	}
	// The digest of a built application is kept.
	built := AppSpec{Name: "app", Port: 8080}
	if err := writeServiceValues(path, built, p); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "docker.io/vaultwarden/server@sha256:") {
		t.Fatalf("the compiler must not touch the digest of a built application:\n%s", raw)
	}
}

// A database that is a file: the addon writes the claim, and the
// application mounts it.
func TestTheProjectWithAVolumeAddonMountsTheDisk(t *testing.T) {
	bare := setupGitOps(t, "")
	manifest := ProjectManifest{
		Project:   "shop",
		Namespace: "shop",
		Addons: map[string]Addon{
			"data": {Engine: "volume", Path: "/data"},
		},
		Services: map[string]Service{
			"app": {Type: "stateless", Image: "example.org/app@sha256:abc", Port: 8080,
				Hostname: "app.shop.bzh", Addons: []string{"data"}},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: manifest, Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/shop/app/values.yaml")
	if !strings.Contains(values, "path: /data") {
		t.Fatalf("the volume of the addon is not in the values of the application:\n%s", values)
	}
	if !strings.Contains(values, "claim: shop-data") {
		t.Fatalf("the claim of the volume is not in the values of the application:\n%s", values)
	}
	claim := readFile(t, clone, "kubernetes/vps/projects/shop/addons/data/volume.yaml")
	if !strings.Contains(claim, "name: shop-data") {
		t.Fatalf("the claim of the addon is missing:\n%s", claim)
	}
}

// Le volume d'un addon a sa reclamation, ecrite par l'addon : le
// kustomization de l'application ne doit pas la lister une seconde fois,
// sous peine de pointer un fichier qui n'existe pas.
func TestTheKustomizationOfAServiceWithAVolumeAddonIsBuildable(t *testing.T) {
	bare := setupGitOps(t, "")
	manifest := ProjectManifest{
		Project:   "shop",
		Namespace: "shop",
		Addons: map[string]Addon{
			"data": {Engine: "volume", Path: "/data"},
		},
		Services: map[string]Service{
			"app": {Type: "stateless", Image: "example.org/app@sha256:abc", Port: 8080,
				Hostname: "app.shop.bzh", Addons: []string{"data"}},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: manifest, Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	kustomization := readFile(t, clone, "kubernetes/vps/projects/shop/app/kustomization.yaml")
	if strings.Contains(kustomization, "data.pvc.yaml") {
		t.Fatalf("the kustomization must not list the claim of the addon:\n%s", kustomization)
	}
}

// Une image tierce attend parfois une commande : le manifeste la declare,
// et elle arrive au conteneur.
func TestTheCommandOfTheManifestReachesTheContainer(t *testing.T) {
	bare := setupGitOps(t, "")
	manifest := ProjectManifest{
		Project:   "shop",
		Namespace: "shop",
		Services: map[string]Service{
			"app": {Type: "stateless", Image: "example.org/app@sha256:abc", Port: 8080,
				Hostname: "app.shop.bzh", Command: []string{"serve", "--listen-http=:80"}},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: manifest, Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/shop/app/values.yaml")
	if !strings.Contains(values, `command:`) || !strings.Contains(values, `"serve"`) {
		t.Fatalf("the command of the manifest is not in the values:\n%s", values)
	}
}

package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A third party application runs a published image instead of a build:
// Immich here, with its database, its cache, its disk and its command.
//
// The manifest already pins the digest: the compilation writes it into the
// values file without any network call, and the service is activated in
// the kustomization of the project exactly like a built one.
func TestProjectPublishedImageWithAddonsAndVolumes(t *testing.T) {
	bare := setupAppsGitOps(t)

	// Already pinned: ResolveDigestWithPolicy returns the reference
	// unchanged, the compilation never touches the network.
	const image = "ghcr.io/immich-app/immich-server@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	m := ProjectManifest{
		Project: "photos",
		Services: map[string]Service{
			"server": {
				Type:     "stateless",
				Image:    image,
				Port:     2283,
				Hostname: "photos.example.org",
				Env: map[string]string{
					"IMMICH_VERSION":   "release",
					"DB_DATABASE_NAME": "immich",
				},
				Command:       []string{"start.sh"},
				Volumes:       []Volume{{Name: "upload", Path: "/data", Size: "10Gi"}},
				StartupBudget: 120,
				Addons:        []string{"photos-db", "photos-cache"},
			},
		},
		Addons: map[string]Addon{
			"photos-db":    {Engine: "cnpg", Database: "immich", Owner: "immich"},
			"photos-cache": {Engine: "valkey", Plan: "small"},
		},
	}
	res, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, AddonOperators: []string{"cnpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 {
		t.Fatalf("apps: %+v", res.Apps)
	}
	app := res.Apps[0]
	if app.Name != "photos-server" {
		t.Fatalf("app name: %q", app.Name)
	}
	if app.ValuesPath != "kubernetes/vps/projects/photos/server/values.yaml" {
		t.Fatalf("values path: %q", app.ValuesPath)
	}
	// A published image is never built: there is no repository, and the
	// operational image is the pinned reference from the manifest.
	if app.Repository != "" {
		t.Fatalf("a published image must not declare a build repository: %+v", app)
	}
	if app.Image != image {
		t.Fatalf("app image: %q", app.Image)
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/photos/server/values.yaml")

	// The pinned digest is written verbatim: the manifest keeps the tag,
	// the generated file holds the digest, like for a built image.
	if !strings.Contains(values, `image: "`+image+`"`) {
		t.Fatalf("the pinned image is not in the values:\n%s", values)
	}
	if !strings.Contains(values, "hostname: photos.example.org") {
		t.Fatalf("the manifest runtime was lost:\n%s", values)
	}
	if !strings.Contains(values, "port: 2283") {
		t.Fatalf("the port was lost:\n%s", values)
	}
	// The database addon injects its URL from the Secret CloudNativePG
	// generates; the value never transits in the repository.
	if !strings.Contains(values, "name: DATABASE_URL") {
		t.Fatalf("the database addon must give its environment:\n%s", values)
	}
	if !strings.Contains(values, "photos-photos-db-app") {
		t.Fatalf("the value must come from the secret of the addon:\n%s", values)
	}
	if !strings.Contains(values, "key: uri") {
		t.Fatalf("the value must be the connection string of the addon:\n%s", values)
	}
	// The cache addon injects a literal URL: no secret needed to reach it.
	if !strings.Contains(values, "name: VALKEY_URL") {
		t.Fatalf("the cache addon must give its environment:\n%s", values)
	}
	if !strings.Contains(values, "valkey://photos-photos-cache:6379") {
		t.Fatalf("the cache URL must point at the addon service:\n%s", values)
	}
	// The literal environment of the manifest survives after the addons'.
	if !strings.Contains(values, "name: IMMICH_VERSION") || !strings.Contains(values, `value: "release"`) {
		t.Fatalf("the manifest env was lost:\n%s", values)
	}
	if !strings.Contains(values, "name: DB_DATABASE_NAME") || !strings.Contains(values, `value: "immich"`) {
		t.Fatalf("the manifest env was lost:\n%s", values)
	}
	// The disk outlives the pod: it is declared in the values and claimed
	// next to the service.
	if !strings.Contains(values, `name: "upload"`) || !strings.Contains(values, `path: "/data"`) || !strings.Contains(values, "size: 10Gi") {
		t.Fatalf("the volume was lost:\n%s", values)
	}
	if _, err := os.Stat(filepath.Join(clone, "kubernetes/vps/projects/photos/server/upload.pvc.yaml")); err != nil {
		t.Fatalf("the sized volume must have its claim: %v", err)
	}
	// The image expects its own command: it is rendered for the chart.
	if !strings.Contains(values, "command:\n  - \"start.sh\"") {
		t.Fatalf("the command was lost:\n%s", values)
	}
	// The startup budget becomes a probe: (120+1)/2 checks, 2s apart.
	if !strings.Contains(values, "failureThreshold: 60") {
		t.Fatalf("the startup budget was lost:\n%s", values)
	}

	// The HelmRelease waits for the declared budget plus the test hook.
	release := readFile(t, clone, "kubernetes/vps/projects/photos/server/helmrelease.yaml")
	if !strings.Contains(release, "timeout: 180s") {
		t.Fatalf("the release must wait for the startup budget:\n%s", release)
	}

	// The service is already pinned, so it is activated in the
	// kustomization of the project, not in the apps parent.
	parent := readFile(t, clone, "kubernetes/vps/projects/photos/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "server") {
		t.Fatalf("the service must be activated in the project:\n%s", parent)
	}
}

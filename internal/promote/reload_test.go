package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedManifest writes a manifest in the GitOps repository, the way a
// developer would.
func seedManifest(t *testing.T, bare string, project string, manifest string) {
	t.Helper()
	seed := t.TempDir()
	gitT(t, "", "clone", bare, seed)
	dir := filepath.Join(seed, "kubernetes", "vps", "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kuberpack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", ".")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "manifest")
	gitT(t, seed, "push", "origin", "HEAD:main")
}

// A manifest written in the repository is compiled: the runtime follows it.
func TestReloadProjectsCompilesTheManifestInTheRepository(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "demo", `project: demo
namespace: demo
services:
  site:
    type: stateless
    repository: owner/repo
    port: 8080
    hostname: site.example.org
    replicas: 2
policies:
  defaultDeny: true
`)

	result, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main",
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 {
		t.Fatalf("projects: %+v", result.Projects)
	}
	project := result.Projects[0]
	if project.Project != "demo" {
		t.Fatalf("project: %q", project.Project)
	}
	if len(project.Services) != 1 || len(project.Apps) != 1 {
		t.Fatalf("services: %+v apps: %+v", project.Services, project.Apps)
	}
	if project.Apps[0].ValuesPath != "kubernetes/vps/projects/demo/site/values.yaml" {
		t.Fatalf("values path: %q", project.Apps[0].ValuesPath)
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "hostname: site.example.org") {
		t.Fatalf("values:\n%s", values)
	}
	if !strings.Contains(values, "replicas: 2") {
		t.Fatalf("values:\n%s", values)
	}
	// The manifest is not rewritten: the comments of the developer survive.
	manifest := readFile(t, clone, "kubernetes/vps/projects/demo/kuberpack.yaml")
	if !strings.Contains(manifest, "project: demo") {
		t.Fatalf("manifest:\n%s", manifest)
	}
}

// A manifest that disappears takes its project with it.
func TestReloadProjectsRemovesTheProjectWithoutManifest(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "demo", `project: demo
services:
  site:
    type: stateless
    repository: owner/repo
    port: 8080
    hostname: site.example.org
`)
	if _, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	// The service is compiled, but not activated: it has no pinned image yet.
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "hostname: site.example.org") {
		t.Fatalf("values:\n%s", values)
	}

	// The developer removes the manifest.
	seed := t.TempDir()
	gitT(t, "", "clone", bare, seed)
	if err := os.Remove(filepath.Join(seed, "kubernetes", "vps", "projects", "demo", "kuberpack.yaml")); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "add", "-A")
	gitT(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "-c", "commit.gpgsign=false", "commit", "-m", "remove manifest")
	gitT(t, seed, "push", "origin", "HEAD:main")

	result, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Projects: []string{"demo"},
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "demo" {
		t.Fatalf("removed: %+v", result.Removed)
	}
	clone = readRemoteTree(t, bare)
	if _, err := os.Stat(filepath.Join(clone, "kubernetes", "vps", "projects", "demo")); err == nil {
		t.Fatal("the project must be deleted")
	}
	parent := readFile(t, clone, "kubernetes/vps/projects/kustomization.yaml")
	if listedInKustomization([]byte(parent), "demo") {
		t.Fatalf("the project must leave the parent:\n%s", parent)
	}
}

// An invalid manifest is an error, not a silent no-op.
func TestReloadProjectsRefusesAnInvalidManifest(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "demo", `project: demo
services:
  site:
    type: worker
`)
	_, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Platform: Platform{NetworkPolicy: true},
	})
	if err == nil {
		t.Fatal("an invalid manifest must be refused")
	}
	if !strings.Contains(err.Error(), "worker") {
		t.Fatalf("the error must name the problem: %v", err)
	}
}

// A recompilation after a pin creates no diff: the file the control plane
// patched and the file the compiler generates are the same one. Otherwise
// every build would trigger a useless commit.
func TestReloadProjectsDoesNotChurnAfterAPin(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "demo", `project: demo
services:
  site:
    type: stateless
    repository: owner/repo
    port: 8080
    hostname: site.example.org
`)
	// The project is compiled first, as the API path does.
	if _, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	ref := mustPin(t)
	if _, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "kubernetes/vps/projects/demo/site/values.yaml",
		ChartPath:    chartDir(t),
		Image:        ref,
		Hostname:     "site.example.org",
		Port:         8080,
		Healthcheck:  "/",
		Platform:     Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Projects: []string{"demo"},
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("a recompilation after a pin must not create a commit")
	}
}

// A push that touches the values of a service recompiles its project, and
// the pinned digest survives.
func TestReloadProjectsKeepsThePinnedDigest(t *testing.T) {
	bare := setupAppsGitOps(t)
	seedManifest(t, bare, "demo", `project: demo
services:
  site:
    type: stateless
    repository: owner/repo
    port: 8080
    hostname: site.example.org
`)
	ref := mustPin(t)
	if _, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   "kubernetes/vps/projects/demo/site/values.yaml",
		ChartPath:    chartDir(t),
		Image:        ref,
		Hostname:     "site.example.org",
		Port:         8080,
		Healthcheck:  "/",
		Platform:     Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}

	// The manifest changes: the hostname follows it, the digest does not move.
	seedManifest(t, bare, "demo", `project: demo
services:
  site:
    type: stateless
    repository: owner/repo
    port: 9090
    hostname: new.example.org
`)
	result, err := ReloadProjects(context.Background(), ReloadRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Projects: []string{"demo"},
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("the change must be committed")
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "hostname: new.example.org") {
		t.Fatalf("the manifest must drive the runtime:\n%s", values)
	}
	if !strings.Contains(values, ref.String()) {
		t.Fatalf("the pinned digest must survive:\n%s", values)
	}
}

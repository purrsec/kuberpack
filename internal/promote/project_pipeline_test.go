package promote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole chain of a project service: the manifest declares it, the
// compiler describes it, the build pins the digest in the values file of the
// service and the service is activated in the kustomization of the project.
//
// The values path is the link: if it is wrong, the digest lands in the legacy
// app directory and the service is never deployed.
func TestProjectServiceIsBuiltDeployedAndActivated(t *testing.T) {
	bare := setupAppsGitOps(t)

	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "site.example.org",
			},
		},
	}
	res, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 {
		t.Fatalf("apps: %+v", res.Apps)
	}
	app := res.Apps[0]
	if app.Name != "demo-site" {
		t.Fatalf("app name: %q", app.Name)
	}
	if app.ValuesPath != "kubernetes/vps/projects/demo/site/values.yaml" {
		t.Fatalf("values path: %q", app.ValuesPath)
	}
	if app.Repository != "owner/repo" || app.Branch != "main" || app.Strategy != "auto" {
		t.Fatalf("build parameters: %+v", app)
	}
	if app.Port != 8080 || app.Healthcheck != "/" {
		t.Fatalf("runtime: %+v", app)
	}

	// The build lands its digest in the values file of the service.
	ref := mustPin(t)
	pinned, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "main",
		ValuesPath:   app.ValuesPath,
		ChartPath:    chartDir(t),
		Image:        ref,
		Hostname:     app.Hostname,
		Port:         app.Port,
		Healthcheck:  app.Healthcheck,
		Platform:     Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Changed {
		t.Fatal("the promote must commit the digest")
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, ref.String()) {
		t.Fatalf("the digest is not in the values of the project:\n%s", values)
	}
	// The runtime contract of the manifest survived the pin.
	if !strings.Contains(values, "hostname: site.example.org") {
		t.Fatalf("the manifest runtime was lost:\n%s", values)
	}

	// The service is activated in the kustomization of the project, not in
	// the apps parent.
	parent := readFile(t, clone, "kubernetes/vps/projects/demo/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "site") {
		t.Fatalf("the service must be activated in the project:\n%s", parent)
	}
	appsParent := readFile(t, clone, "kubernetes/vps/apps/kustomization.yaml")
	if listedInKustomization([]byte(appsParent), "site") {
		t.Fatalf("the service must not leak into the apps parent:\n%s", appsParent)
	}
	// The legacy app directory must not be created for a project service.
	if _, err := os.Stat(filepath.Join(clone, "kubernetes/vps/apps/site")); err == nil {
		t.Fatal("a project service must not create a legacy app directory")
	}
}

// A change in the manifest reaches the deployment, and the pinned digest
// survives it: the manifest owns the runtime, the control plane owns the
// image.
func TestProjectManifestChangeUpdatesTheRuntimeAndKeepsTheDigest(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"site": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "site.example.org",
				Replicas:   intPtr(1),
			},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
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

	// The manifest changes the runtime.
	m.Services["site"] = Service{
		Type:       "stateless",
		Repository: "owner/repo",
		Port:       9090,
		Hostname:   "new.example.org",
		Replicas:   intPtr(3),
		Env:        map[string]string{"NODE_ENV": "production"},
	}
	res, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("the change of the manifest must be committed")
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/site/values.yaml")
	if !strings.Contains(values, "hostname: new.example.org") {
		t.Fatalf("the hostname must follow the manifest:\n%s", values)
	}
	if !strings.Contains(values, "port: 9090") {
		t.Fatalf("the port must follow the manifest:\n%s", values)
	}
	if !strings.Contains(values, "replicas: 3") {
		t.Fatalf("the replicas must follow the manifest:\n%s", values)
	}
	if !strings.Contains(values, "name: NODE_ENV") {
		t.Fatalf("the environment must follow the manifest:\n%s", values)
	}
	// The digest is still there: the control plane owns it.
	if !strings.Contains(values, ref.String()) {
		t.Fatalf("the pinned digest must survive a manifest change:\n%s", values)
	}
}

// A service without a repository is never built: it stays in the GitOps and
// it produces no app.
func TestProjectServiceWithoutASourceIsRefused(t *testing.T) {
	bare := setupAppsGitOps(t)
	_, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main",
		Manifest: ProjectManifest{Project: "demo", Services: map[string]Service{
			"site": {Type: "stateless", Port: 8080, Hostname: "site.example.org"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "repository to build, or an image to run") {
		t.Fatalf("a source-less service must fail before any commit: %v", err)
	}
}

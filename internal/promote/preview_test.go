package promote

import (
	"context"
	"strings"
	"testing"
)

func TestPreviewBranchPinAndPrune(t *testing.T) {
	bare := setupAppsGitOps(t)
	if err := EnsurePreviewBranch(context.Background(), bare, "previews", ""); err != nil {
		t.Fatal(err)
	}
	platform := Platform{NetworkPolicy: true, IngressTLS: true, PreviewDomain: "preview.example.org"}
	spec := AppSpec{Name: "site-pr-3", Hostname: "pr-3.site.preview.example.org", Port: 8080, Healthcheck: "/", Preview: true}

	result, err := Run(context.Background(), Request{
		GitOpsURL:    bare,
		GitOpsBranch: "previews",
		ValuesPath:   platform.ForPreview().ValuesPath(spec.Name),
		ChartPath:    chartDir(t),
		Image:        mustPin(t),
		Hostname:     spec.Hostname,
		Port:         spec.Port,
		Healthcheck:  spec.Healthcheck,
		Platform:     platform.ForPreview(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected preview pin")
	}
	clone := t.TempDir()
	gitT(t, "", "clone", "--branch", "previews", bare, clone)
	hr := readFile(t, clone, "kubernetes/vps/previews/site-pr-3/helmrelease.yaml")
	if !strings.Contains(hr, "name: site-pr-3") || !strings.Contains(hr, "enable: true") {
		t.Fatalf("preview helmrelease:\n%s", hr)
	}
	values := readFile(t, clone, "kubernetes/vps/previews/site-pr-3/values.yaml")
	if !strings.Contains(values, "cpu: 200m") {
		t.Fatalf("preview must use a lower quota:\n%s", values)
	}
	if strings.Contains(values, "app-site\n") {
		t.Fatalf("preview must not mount the production secret by default:\n%s", values)
	}
	if !strings.Contains(values, "name: app-site-pr-3") || !strings.Contains(values, "name: dev") {
		t.Fatalf("preview should mount its exact secret and the dev fallback:\n%s", values)
	}
	if strings.Contains(values, "postgres:") || strings.Contains(values, "- billing") {
		t.Fatalf("preview must not copy production peers:\n%s", values)
	}
	parent := readFile(t, clone, "kubernetes/vps/previews/kustomization.yaml")
	if !listedInKustomization([]byte(parent), "site-pr-3") {
		t.Fatalf("preview not enabled:\n%s", parent)
	}
	prod := readFile(t, readRemoteTree(t, bare), "kubernetes/vps/apps/kustomization.yaml")
	if listedInKustomization([]byte(prod), "site-pr-3") {
		t.Fatal("preview must not land on main apps")
	}

	rm, err := RemovePreview(context.Background(), PreviewRequest{
		GitOpsURL:    bare,
		GitOpsBranch: "previews",
		App:          spec,
		Platform:     platform,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rm.Changed {
		t.Fatal("expected prune commit")
	}
	clone = t.TempDir()
	gitT(t, "", "clone", "--branch", "previews", bare, clone)
	parent = readFile(t, clone, "kubernetes/vps/previews/kustomization.yaml")
	if listedInKustomization([]byte(parent), "site-pr-3") {
		t.Fatalf("preview still listed:\n%s", parent)
	}
}

func TestPreviewHostname(t *testing.T) {
	if got := PreviewHostname("web", 7, "preview.host.bzh"); got != "pr-7.web.preview.host.bzh" {
		t.Fatalf("got %q", got)
	}
	if PreviewReleaseName("web", 7) != "web-pr-7" {
		t.Fatal("release name")
	}
}

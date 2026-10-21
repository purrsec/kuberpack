package promote

import (
	"context"
	"strings"
	"testing"
)

// A manifest must not be able to declare something the compiler ignores: an
// unknown field is an error, not a silent no-op.
func TestParseProjectManifestRefusesUnknownFields(t *testing.T) {
	raw := `
project: demo
services:
  app:
    repository: owner/repo
    port: 8080
    publish_directory: dist
`
	_, err := ParseProjectManifest([]byte(raw))
	if err == nil {
		t.Fatal("an unknown field must be refused")
	}
	if !strings.Contains(err.Error(), "publish_directory") {
		t.Fatalf("the error must name the field: %v", err)
	}
}

// The refusals are explicit: a manifest that cannot be compiled as written
// fails instead of compiling into something else.
func TestParseProjectManifestRefusesUnsupported(t *testing.T) {
	cases := map[string]string{
		"unsupported service type": `
project: demo
services:
  app:
    type: worker
`,
		"unsupported addon engine": `
project: demo
addons:
  valkey:
    engine: statefulset
`,
		"unsupported backup profile": `
project: demo
addons:
  postgres:
    engine: cnpg
    backup:
      s3: garage
`,
		"unknown peer": `
project: demo
services:
  app: {}
policies:
  peers: [billing]
`,
		"invalid strategy": `
project: demo
services:
  app:
    strategy: nixpacks
`,
		"invalid repository": `
project: demo
services:
  app:
    repository: notrepo
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseProjectManifest([]byte(raw)); err == nil {
				t.Fatalf("%s must be refused", name)
			}
		})
	}
}

// A manifest that only uses what is supported is accepted.
func TestParseProjectManifestAcceptsTheSupportedFields(t *testing.T) {
	raw := `
project: demo
namespace: demo
services:
  app:
    type: stateless
    repository: owner/repo
    branch: main
    strategy: auto
    port: 8080
    hostname: app.example.org
    healthcheck: /healthz
    replicas: 3
    env:
      NODE_ENV: production
    build:
      root_directory: frontend
      build_command: npm run build
      start_command: npm run start
    secrets: [API_KEY]
    secret_env: prod
addons:
  postgres:
    engine: cnpg
    database: app
    backup:
      retention: 30d
policies:
  defaultDeny: true
  internet: true
  peers: [postgres]
`
	m, err := ParseProjectManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	svc := m.Services["app"]
	if svc.Replicas != 3 || svc.Env["NODE_ENV"] != "production" {
		t.Fatalf("service: %+v", svc)
	}
	if svc.Build == nil || svc.Build.RootDirectory != "frontend" {
		t.Fatalf("build: %+v", svc.Build)
	}
	if m.Addons["postgres"].Backup == nil || m.Addons["postgres"].Backup.Retention != "30d" {
		t.Fatalf("backup: %+v", m.Addons["postgres"].Backup)
	}
	if !m.Policies.defaultDenyEnabled() {
		t.Fatal("defaultDeny must be true")
	}
}

// The manifest drives the runtime contract of the service: replicas,
// environment, isolation.
func TestProjectCompilesTheRuntimeContract(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"app": {
				Type:       "stateless",
				Repository: "owner/repo",
				Port:       8080,
				Hostname:   "app.example.org",
				Replicas:   3,
				Env:        map[string]string{"NODE_ENV": "production", "API_URL": "https://x"},
			},
		},
		Policies: Policy{DefaultDeny: BoolPtr(true), Peers: []string{}},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, IngressTLS: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml")

	if !strings.Contains(values, "replicas: 3") {
		t.Fatalf("replicas:\n%s", values)
	}
	// A service without a declared count runs one instance, never zero.
	if strings.Contains(values, "replicas: 0") {
		t.Fatalf("replicas:\n%s", values)
	}
	if !strings.Contains(values, "env:") || !strings.Contains(values, "name: API_URL") {
		t.Fatalf("env:\n%s", values)
	}
	// The environment is sorted: the compiler must be deterministic.
	if strings.Index(values, "name: API_URL") > strings.Index(values, "name: NODE_ENV") {
		t.Fatalf("the env list must be sorted:\n%s", values)
	}
	if !strings.Contains(values, "networkPolicy:\n  enabled: true") {
		t.Fatalf("networkPolicy:\n%s", values)
	}
}

// A service without a declared replica count runs one instance.
func TestProjectServiceWithoutReplicasRunsOneInstance(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"app": {Type: "stateless", Port: 8080, Hostname: "app.example.org"},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(values, "replicas: 1") {
		t.Fatalf("replicas:\n%s", values)
	}
}

// defaultDeny: false opts a service out of the NetworkPolicy.
func TestProjectWithoutDefaultDenyHasNoNetworkPolicy(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"app": {Type: "stateless", Port: 8080, Hostname: "app.example.org"},
		},
		Policies: Policy{DefaultDeny: BoolPtr(false)},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(values, "networkPolicy:\n  enabled: false") {
		t.Fatalf("networkPolicy:\n%s", values)
	}
}

// The namespace of the project is the namespace of its resources, and the
// project creates it.
func TestProjectNamespaceIsTheNamespaceOfItsResources(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project:   "demo",
		Namespace: "demo-ns",
		Services: map[string]Service{
			"app": {Type: "stateless", Port: 8080, Hostname: "app.example.org"},
		},
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "app"},
		},
		Policies: Policy{Peers: []string{"postgres"}},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true, ReleaseNamespace: "apps"},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)

	ns := readFile(t, clone, "kubernetes/vps/projects/demo/namespace.yaml")
	if !strings.Contains(ns, "name: demo-ns") {
		t.Fatalf("namespace:\n%s", ns)
	}
	parent := readFile(t, clone, "kubernetes/vps/projects/demo/kustomization.yaml")
	if !strings.Contains(parent, "namespace: demo-ns") {
		t.Fatalf("parent:\n%s", parent)
	}
	if !listedInKustomization([]byte(parent), "namespace.yaml") {
		t.Fatalf("the namespace must be applied with the project:\n%s", parent)
	}
	release := readFile(t, clone, "kubernetes/vps/projects/demo/app/helmrelease.yaml")
	if !strings.Contains(release, "namespace: demo-ns") {
		t.Fatalf("helmrelease:\n%s", release)
	}
	db := readFile(t, clone, "kubernetes/vps/projects/demo/postgres/database.yaml")
	if !strings.Contains(db, "namespace: demo-ns") {
		t.Fatalf("database:\n%s", db)
	}
}

// The backup retention of an addon reaches the CNPG cluster.
func TestProjectAddonBackupRetention(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Addons: map[string]Addon{
			"postgres": {Engine: "cnpg", Database: "app", Backup: &Backup{Retention: "30d"}},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{NetworkPolicy: true},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	db := readFile(t, clone, "kubernetes/vps/projects/demo/postgres/database.yaml")
	if !strings.Contains(db, "retentionPolicy: 30d") {
		t.Fatalf("database:\n%s", db)
	}
}

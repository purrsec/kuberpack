package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	gitprovider "git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"git.host.bzh/pepe/kuberpack/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func v2Fixture(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	st := tokenTestStore(t)
	tokens := map[string]string{"admin": "root"}
	for _, role := range []string{"reader", "deployer", "maintainer"} {
		_, token, err := st.CreateAPIToken(context.Background(), role, "demo", role, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	s := New(Config{Store: st, APIToken: "root", GitOpsURL: setupControlProjectsRepo(t), GitOpsBranch: "main", Provider: mustProvider(t, "https://git.example.org", nil)})
	policy, _ := json.Marshal(api.ProjectPolicy{Namespace: "demo", MaxServices: 20, MaxReplicas: 10, Domains: []string{"example.org"}, Repositories: []string{"demo/web", "acme/api"}})
	if err := st.CreateObject(context.Background(), store.Object{Kind: "policy", ID: "demo", Project: "demo", Owner: "admin", Payload: policy}); err != nil {
		t.Fatal(err)
	}

	return s, tokens
}
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestV2ScopesApplyToOldAndNewRoutes(t *testing.T) {
	s, tokens := v2Fixture(t)
	seedTokenApps(t, s.cfg.Store)
	for _, role := range []string{"reader", "deployer", "maintainer"} {
		for _, path := range []string{"/api/v2/session", "/api/v2/projects/demo", "/api/v1/apps/demo-web/status"} {
			r := tokenTestRequest(t, s.Handler(), http.MethodGet, path, tokens[role], "")
			if r.Code != 200 {
				t.Fatalf("%s %s: %d %s", role, path, r.Code, r.Body.String())
			}
		}
		for _, path := range []string{"/api/v1/projects/demo", "/api/v1/projects/other"} {
			r := tokenTestRequest(t, s.Handler(), http.MethodDelete, path, tokens[role], "")
			if r.Code != 403 {
				t.Fatalf("%s destroyed project via %s: %d", role, path, r.Code)
			}
		}
		if r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v1/apps/demo-web/redeploy", tokens[role], ""); r.Code != 410 {
			t.Fatalf("legacy redeploy must be retired: %d", r.Code)
		}
	}
	for _, role := range []string{"reader", "deployer"} {
		r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens[role], jsonBody(t, api.PlanRequest{Action: "deploy", Manifest: "project: demo\n"}))
		if r.Code != 403 {
			t.Fatalf("%s can publish a manifest: %d %s", role, r.Code, r.Body.String())
		}
	}
	for _, role := range []string{"reader", "deployer", "maintainer"} {
		r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens[role], jsonBody(t, api.PlanRequest{Action: "destroy"}))
		if r.Code != 403 {
			t.Fatalf("%s can prepare destruction: %d", role, r.Code)
		}
	}
}

func TestV2PlanIsReadOnlyAndAcceptanceIsIdempotent(t *testing.T) {
	s, tokens := v2Fixture(t)
	before := remoteGitHead(t, s.cfg.GitOpsURL)
	manifest := "project: demo\nservices:\n  web:\n    image: docker.io/library/nginx@sha256:" + strings.Repeat("a", 64) + "\n    hostname: demo.example.org\n    port: 8080\n"
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens["maintainer"], jsonBody(t, api.PlanRequest{Action: "deploy", Manifest: manifest}))
	if r.Code != 201 {
		t.Fatalf("plan: %d %s", r.Code, r.Body.String())
	}
	var plan api.Plan
	if err := json.Unmarshal(r.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if remoteGitHead(t, s.cfg.GitOpsURL) != before {
		t.Fatal("preparing a plan wrote GitOps")
	}
	if apps, _ := s.cfg.Store.ListImageApps(context.Background()); len(apps) != 0 {
		t.Fatal("preparing a plan registered an application")
	}
	if ds, _ := s.cfg.Store.QueuedDeliveries(context.Background()); len(ds) != 0 {
		t.Fatal("preparing a plan queued a build")
	}
	request := api.DeploymentRequest{PlanID: plan.ID, RequestID: "pipeline-1"}
	var first api.Deployment
	for i := 0; i < 2; i++ {
		r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/deployments", tokens["maintainer"], jsonBody(t, request))
		if r.Code != 202 && r.Code != 200 {
			t.Fatalf("execute: %d %s", r.Code, r.Body.String())
		}
		var got api.Deployment
		_ = json.Unmarshal(r.Body.Bytes(), &got)
		if i == 0 {
			first = got
		} else if got.ID != first.ID {
			t.Fatal("same request duplicated deployment")
		}
	}
	objects, err := s.cfg.Store.Objects(context.Background(), "deployment", "demo")
	if err != nil || len(objects) != 1 {
		t.Fatalf("operations: %d %v", len(objects), err)
	}
	request.PlanID = "different"
	r = tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/deployments", tokens["maintainer"], jsonBody(t, request))
	if r.Code != 409 {
		t.Fatalf("different request content must conflict: %d", r.Code)
	}
	if remoteGitHead(t, s.cfg.GitOpsURL) != before {
		t.Fatal("HTTP acceptance must not perform work before durable processing")
	}
	// A different credential cannot execute this approved plan.
	_, other, err := s.cfg.Store.CreateAPIToken(context.Background(), "other maintainer", "demo", "maintainer", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r = tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/deployments", other, jsonBody(t, api.DeploymentRequest{PlanID: plan.ID, RequestID: "another"}))
	if r.Code != 404 {
		t.Fatalf("plan replay by another credential: %d", r.Code)
	}
}

func TestDestroyingAnEmptyReservationRevokesTokensWithoutChangingGitOps(t *testing.T) {
	s, tokens := v2Fixture(t)
	ctx := context.Background()
	s.cfg.Kube = fake.NewSimpleClientset()
	s.cfg.KubeDynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s.cfg.TenantIsolation = true
	before := remoteGitHead(t, s.cfg.GitOpsURL)
	reserved := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/admin/projects/unused", tokens["admin"], "")
	if reserved.Code != http.StatusCreated {
		t.Fatalf("reserve: %d %s", reserved.Code, reserved.Body.String())
	}
	_, reader, err := s.cfg.Store.CreateAPIToken(ctx, "unused reader", "unused", "reader", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/unused/plans", tokens["admin"], jsonBody(t, api.PlanRequest{Action: "destroy"}))
	if r.Code != http.StatusCreated {
		t.Fatalf("plan: %d %s", r.Code, r.Body.String())
	}
	var plan api.Plan
	if err := json.Unmarshal(r.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	r = tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/unused/deployments", tokens["admin"], jsonBody(t, api.DeploymentRequest{PlanID: plan.ID, RequestID: "remove-reservation", ConfirmProject: "unused"}))
	if r.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", r.Code, r.Body.String())
	}
	var accepted api.Deployment
	if err := json.Unmarshal(r.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	object, err := s.cfg.Store.Object(ctx, "deployment", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operation savedDeployment
	if err := json.Unmarshal(object.Payload, &operation); err != nil {
		t.Fatal(err)
	}
	if err := s.advanceDeployment(ctx, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" {
		t.Fatalf("reservation removal did not finish: %+v", operation)
	}
	if _, err := s.cfg.Store.Object(ctx, "policy", "unused"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reservation remains: %v", err)
	}
	if _, err := s.cfg.Store.AuthenticateAPIToken(ctx, reader, time.Now()); err == nil {
		t.Fatal("removed project's token remains usable")
	}
	if remoteGitHead(t, s.cfg.GitOpsURL) != before {
		t.Fatal("removing an unpublished reservation changed GitOps")
	}
}

func remoteGitHead(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repo, "rev-parse", "HEAD")
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestV2ExpiredAndStalePlansCannotExecute(t *testing.T) {
	s, tokens := v2Fixture(t)
	p, _ := s.cfg.Store.AuthenticateAPIToken(context.Background(), tokens["maintainer"], time.Now())
	principal := Principal{Name: p.Name, TokenID: p.ID}
	raw, m, err := s.projectManifest(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	_, policyHash, err := s.policyFor(context.Background(), "demo", m)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, base string
		expires    time.Time
	}{{"expired", spec.HashBytes(raw), time.Now().Add(-time.Minute)}, {"stale", "outdated", time.Now().Add(time.Minute)}} {
		plan := api.Plan{ID: tc.name, Project: "demo", Action: "deploy", BaseRevision: tc.base, PolicyRevision: policyHash, ExpiresAt: tc.expires}
		b, _ := json.Marshal(plan)
		if err := s.cfg.Store.CreateObject(context.Background(), store.Object{Kind: "plan", ID: plan.ID, Project: "demo", Owner: principal.ownerKey(), Payload: b}); err != nil {
			t.Fatal(err)
		}
		r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/deployments", tokens["maintainer"], jsonBody(t, api.DeploymentRequest{PlanID: plan.ID, RequestID: tc.name}))
		if r.Code != 409 {
			t.Fatalf("%s: %d %s", tc.name, r.Code, r.Body.String())
		}
	}
}

func TestPersistentAndOperatorSettingsCannotBeChangedByManifest(t *testing.T) {
	old := promote.ProjectManifest{Project: "demo", Services: map[string]promote.Service{"web": {Repository: "acme/web", Volumes: []promote.Volume{{Name: "data", Path: "/data", Size: "1Gi"}}}}}
	policy := api.ProjectPolicy{Namespace: "demo", MaxServices: 10, MaxReplicas: 5}
	for _, change := range []func(*promote.ProjectManifest){
		func(m *promote.ProjectManifest) { m.Services = map[string]promote.Service{} },
		func(m *promote.ProjectManifest) { v := m.Services["web"]; v.Volumes = nil; m.Services["web"] = v },
		func(m *promote.ProjectManifest) { m.Namespace = "other" },
		func(m *promote.ProjectManifest) { m.Infisical = &promote.InfisicalScope{ProjectID: "other"} },
		func(m *promote.ProjectManifest) { v := m.Services["web"]; v.RunAsRoot = true; m.Services["web"] = v },
	} {
		b, _ := json.Marshal(old)
		var proposed promote.ProjectManifest
		_ = json.Unmarshal(b, &proposed)
		change(&proposed)
		if err := checkManifestChange(old, proposed, policy); err == nil {
			t.Fatal("unsafe change was accepted")
		}
	}
}

func TestDeploymentCannotSucceedFromAnOldHealthyWorkload(t *testing.T) {
	st := tokenTestStore(t)
	d := statusDeployment()
	release := statusRelease()
	d.Spec.Template.Annotations = map[string]string{"kuberpack.io/configuration-revision": "old"}
	if err := st.SyncImageApps(context.Background(), "demo", []store.App{{Name: "demo-web", Project: "demo", Namespace: "custom", Image: "registry/app@sha256:current"}}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Store: st, Kube: fake.NewSimpleClientset(d), KubeDynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), release)})
	op := savedDeployment{Deployment: api.Deployment{Project: "demo", Status: "deploying", Step: "awaiting_reconciliation", Targets: []api.Target{{Service: "web", App: "demo-web", Image: "registry/app@sha256:current", ConfigurationRevision: "new"}}}}
	if err := s.advanceDeployment(context.Background(), &op); err != nil {
		t.Fatal(err)
	}
	if op.Status == "succeeded" {
		t.Fatal("old configuration was treated as the requested deployment")
	}
	d.Spec.Template.Annotations["kuberpack.io/configuration-revision"] = "new"
	if _, err := s.cfg.Kube.AppsV1().Deployments("custom").Update(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.advanceDeployment(context.Background(), &op); err != nil {
		t.Fatal(err)
	}
	if op.Status != "succeeded" {
		t.Fatalf("matching current configuration should succeed: %+v", op)
	}
}

func TestDestructionRequiresLiveStorageInventory(t *testing.T) {
	s := New(Config{Kube: fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo", Labels: map[string]string{"kuberpack.io/project": "demo"}}}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "standalone-disk", Namespace: "demo"}})})
	if err := s.checkDestroy(context.Background(), "demo", promote.ProjectManifest{Project: "demo"}, "demo"); err == nil {
		t.Fatal("a standalone PVC must block destruction even without addons")
	}
	s.cfg.Kube = nil
	if err := s.checkDestroy(context.Background(), "demo", promote.ProjectManifest{}, "demo"); err == nil {
		t.Fatal("unavailable inventory must fail closed")
	}
}

func TestTokenRequiresExplicitRoleAndExpiryAndRestrictionsSurviveAuthentication(t *testing.T) {
	s, tokens := v2Fixture(t)
	for _, body := range []string{`{"name":"bad"}`, `{"name":"bad","role":"admin"}`, `{"name":"bad","role":"reader","expires_in":"1h"}`} {
		r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/admin/tokens", tokens["admin"], body)
		if r.Code != 400 {
			t.Fatalf("unsafe token defaults: %d %s", r.Code, r.Body.String())
		}
	}
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/admin/tokens", tokens["admin"], `{"name":"ci","role":"deployer","project":"demo","services":["web"],"expires_in":"1h"}`)
	if r.Code != 201 {
		t.Fatalf("token: %d %s", r.Code, r.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &created)
	token, err := s.cfg.Store.AuthenticateAPIToken(context.Background(), created.Token, time.Now())
	if err != nil || !reflect.DeepEqual(token.Services, []string{"web"}) {
		t.Fatalf("scopes were lost: %+v %v", token, err)
	}
	r = tokenTestRequest(t, s.Handler(), http.MethodGet, "/api/v1/projects/demo", created.Token, "")
	if r.Code != 403 {
		t.Fatal("service token can read other services' manifest")
	}
}

func TestPlannedImageDeploymentCompilesRegistersAndWaitsForItsWorkload(t *testing.T) {
	s, tokens := v2Fixture(t)
	manifest := "project: demo\nservices:\n  web:\n    image: docker.io/library/nginx@sha256:" + strings.Repeat("b", 64) + "\n    hostname: demo.example.org\n    port: 8080\n"
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens["maintainer"], jsonBody(t, api.PlanRequest{Action: "deploy", Manifest: manifest}))
	if r.Code != 201 {
		t.Fatalf("plan: %d %s", r.Code, r.Body.String())
	}
	var plan api.Plan
	_ = json.Unmarshal(r.Body.Bytes(), &plan)
	r = tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/deployments", tokens["maintainer"], jsonBody(t, api.DeploymentRequest{PlanID: plan.ID, RequestID: "integration"}))
	if r.Code != 202 {
		t.Fatalf("accept: %d %s", r.Code, r.Body.String())
	}
	var accepted api.Deployment
	_ = json.Unmarshal(r.Body.Bytes(), &accepted)
	o, err := s.cfg.Store.Object(context.Background(), "deployment", accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	var operation savedDeployment
	_ = json.Unmarshal(o.Payload, &operation)
	s.cfg.Kube = fake.NewSimpleClientset()
	s.cfg.KubeDynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := s.advanceDeployment(context.Background(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "deploying" || operation.InfraCommit == "" {
		t.Fatalf("work was not tracked: %+v", operation)
	}
	if _, err := s.cfg.Store.ImageAppByName(context.Background(), "demo-web"); err != nil {
		t.Fatal("image service not registered", err)
	}
	if queued, err := s.cfg.Store.QueuedDeliveries(context.Background()); err != nil || len(queued) != 0 {
		t.Fatalf("published image must not create source builds: %+v %v", queued, err)
	}
	if err := s.advanceDeployment(context.Background(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status == "succeeded" {
		t.Fatal("GitOps commit alone was treated as runtime success")
	}
	d := statusDeployment()
	d.Namespace = "demo"
	d.Spec.Template.Annotations = map[string]string{"kuberpack.io/configuration-revision": plan.Targets[0].ConfigurationRevision}
	d.Spec.Template.Spec.Containers[0].Image = plan.Targets[0].Image
	release := statusRelease()
	release.SetNamespace("demo")
	if _, err := s.cfg.Kube.AppsV1().Deployments("demo").Create(context.Background(), d, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cfg.KubeDynamic.Resource(schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}).Namespace("demo").Create(context.Background(), release, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.advanceDeployment(context.Background(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" {
		t.Fatalf("matching workload was not recognized: %+v", operation)
	}
}

type plannedBranchProvider struct{ gitprovider.Provider }

func (p plannedBranchProvider) BranchSHA(context.Context, string, string, string) (string, error) {
	return strings.Repeat("d", 40), nil
}

func TestServiceRestrictedPlanDoesNotExposeTheCompleteManifest(t *testing.T) {
	s, tokens := v2Fixture(t)
	m := promote.ProjectManifest{Project: "demo", Namespace: "demo", Services: map[string]promote.Service{
		"web":            {Repository: "demo/web", Branch: "main", Hostname: "web.example.org", Port: 8080},
		"private-worker": {Repository: "private/worker", Branch: "main", Hostname: "worker.example.org", Port: 8080, Env: map[string]string{"PRIVATE_MARKER": "not-for-this-service"}},
	}}
	req := s.requestProject(context.Background(), "demo")
	req.Manifest = m
	if _, err := promote.EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	token, err := s.cfg.Store.AuthenticateAPIToken(context.Background(), tokens["deployer"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Store.SetAPITokenServices(context.Background(), token.ID, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	s.cfg.Provider = plannedBranchProvider{s.cfg.Provider}
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens["deployer"], jsonBody(t, api.PlanRequest{Action: "redeploy", Service: "web"}))
	if r.Code != 201 {
		t.Fatalf("plan: %d %s", r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "PRIVATE_MARKER") || strings.Contains(r.Body.String(), "not-for-this-service") || strings.Contains(r.Body.String(), "private/worker") {
		t.Fatal("service-restricted plan exposed another service's configuration")
	}
	r = tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/plans", tokens["deployer"], jsonBody(t, api.PlanRequest{Action: "redeploy", Service: "private-worker"}))
	if r.Code != 403 {
		t.Fatalf("restricted token selected another service: %d", r.Code)
	}
}

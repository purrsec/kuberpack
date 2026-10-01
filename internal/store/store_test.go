package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppsAndDeliveries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "kuberpack.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	app, err := s.InsertApp(ctx, App{
		Name:              "web",
		ForgejoRepository: "Pepe/Hello-World",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		Hostname:          "web.example.org",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.ID == 0 || app.ForgejoRepository != "pepe/hello-world" {
		t.Fatalf("%+v", app)
	}

	got, err := s.AppByRepository(ctx, "pepe/hello-world")
	if err != nil || got.Name != "web" {
		t.Fatalf("%+v %v", got, err)
	}

	if err := s.InsertDelivery(ctx, Delivery{
		ID:        "del-1",
		AppID:     app.ID,
		EventType: "push",
		CommitSHA: "abc",
		Status:    "queued",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDelivery(ctx, Delivery{
		ID:        "del-1",
		AppID:     app.ID,
		EventType: "push",
		CommitSHA: "abc",
		Status:    "queued",
	}); err != ErrDuplicate {
		t.Fatalf("duplicate: %v", err)
	}

	queued, err := s.QueuedDeliveries(ctx)
	if err != nil || len(queued) != 1 {
		t.Fatalf("%v %v", queued, err)
	}

	id, err := s.InsertBuild(ctx, Build{
		AppID:       app.ID,
		DeliveryID:  "del-1",
		Environment: "production",
		CommitSHA:   "abc",
		Status:      "running",
	})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	if err := s.FinishBuild(ctx, id, Build{Status: "succeeded", ImageDigest: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	gotBuild, err := s.SucceededBuildByCommit(ctx, app.ID, "abc")
	if err != nil || gotBuild.ImageDigest != "sha256:x" {
		t.Fatalf("%+v %v", gotBuild, err)
	}
	if _, err := s.SucceededBuildByCommit(ctx, app.ID, "ffff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := s.MarkRolledBack(ctx, id, "helm test failed"); err != nil {
		t.Fatal(err)
	}
	builds, err := s.ListBuilds(ctx, app.ID)
	if err != nil || len(builds) != 1 || builds[0].Status != "rolled_back" {
		t.Fatalf("%+v %v", builds, err)
	}
	if err := s.UpsertPreview(ctx, Preview{AppID: app.ID, PullRequest: 4, HeadSHA: "abc", Hostname: "pr-4.web.example.org", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	prev, err := s.Preview(ctx, app.ID, 4)
	if err != nil || prev.Hostname != "pr-4.web.example.org" {
		t.Fatalf("%+v %v", prev, err)
	}
}

func TestSecretsDir(t *testing.T) {
	d := SecretsDir(filepath.Join(t.TempDir(), "secrets"))
	if err := d.Put(3, "s3cret"); err != nil {
		t.Fatal(err)
	}
	got, err := d.Get(3)
	if err != nil || got != "s3cret" {
		t.Fatalf("%q %v", got, err)
	}
	if err := d.PutDeployKey(3, "-----BEGIN OPENSSH PRIVATE KEY-----\n"); err != nil {
		t.Fatal(err)
	}
	key, err := d.GetDeployKey(3)
	if err != nil || !strings.Contains(key, "OPENSSH") {
		t.Fatalf("%q %v", key, err)
	}
}

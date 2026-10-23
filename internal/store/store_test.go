package store

import (
	"context"
	"errors"
	"path/filepath"
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
		Internet:          true,
		Hostname:          "web.example.org",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.ID == 0 || app.ForgejoRepository != "pepe/hello-world" || !app.Internet {
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
	app.Internet = false
	app.Peers = []string{"billing", "postgres:web"}
	if err := s.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	gotApp, err := s.AppByID(ctx, app.ID)
	if err != nil || gotApp.Internet || len(gotApp.Peers) != 2 || gotApp.Peers[0] != "billing" {
		t.Fatalf("%+v %v", gotApp, err)
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
}

func TestNamespaceMigrationPreservesExistingAppsAndRoundTripsUpdates(t *testing.T) {
	file := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	app, err := s.InsertApp(ctx, App{Name: "demo-web", Project: "demo", ForgejoRepository: "owner/web", ProductionBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the schema of an installation predating deployment observation.
	if _, err := s.db.Exec(`ALTER TABLE apps DROP COLUMN namespace`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AppByName(ctx, app.Name)
	if err != nil || got.ID != app.ID || got.ForgejoRepository != "owner/web" || got.Namespace != "" {
		t.Fatalf("legacy app lost during migration: %+v %v", got, err)
	}
	got.Namespace = "custom-runtime"
	if _, created, err := s.UpsertApp(ctx, got); err != nil || created {
		t.Fatalf("namespace reconciliation: created=%v err=%v", created, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	apps, err := s.ListApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].Namespace != "custom-runtime" {
		t.Fatalf("namespace not persisted: %+v %v", apps, err)
	}
}

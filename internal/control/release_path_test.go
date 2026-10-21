package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

// The GitOps directory is keyed by the platform app name, never by the Forgejo
// repository name: registration wrote <apps>/<app>/, so the promotion must
// pin the very same file.
func TestProductionPromotionTargetsTheAppDirectory(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app, err := st.InsertApp(ctx, store.App{
		Name:              "web",
		ForgejoRepository: "pepe/hello-world",
		ProductionBranch:  "main",
		Strategy:          "auto",
		Autodeploy:        true,
		Hostname:          "web.host.bzh",
		Port:              8080,
		Healthcheck:       "/",
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := mustProvider(t, "https://git.invalid", nil)
	got := make(chan string, 1)
	srv := New(Config{
		Store:    st,
		Secrets:  store.SecretsDir(filepath.Join(dir, "secrets")),
		Provider: provider,
		APIToken: "api",
		Platform: promote.Platform{AppsDir: "kubernetes/vps/apps"},
		Run: func(ctx context.Context, req release.Request) (release.Result, error) {
			got <- req.ValuesPath
			ref, err := image.Pin("git.host.bzh/pepe/hello-world", req.SHA, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			if err != nil {
				return release.Result{}, err
			}
			return release.Result{SHA: req.SHA, Image: ref, InfraCommitSHA: "infra"}, nil
		},
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(workerCtx)

	if err := st.InsertDelivery(ctx, store.Delivery{
		ID: "d1", AppID: app.ID, EventType: "push",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Status: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	srv.kick()

	select {
	case vp := <-got:
		want := "kubernetes/vps/apps/web/values.yaml"
		if vp != want {
			t.Fatalf("ValuesPath = %q, want %q", vp, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not run")
	}
}

// delivery_id is the primary key of webhook_deliveries: it must be unique per
// application. Two apps built from the same commit would otherwise collide and
// the second one would never be built.
func TestDeliveryIDIsScopedToTheApp(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, name := range []string{"web", "api"} {
		app, err := st.InsertApp(ctx, store.App{
			Name:              name,
			ForgejoRepository: "pepe/" + name,
			ProductionBranch:  "main",
			Strategy:          "auto",
			Hostname:          name + ".host.bzh",
			Port:              8080,
			Healthcheck:       "/",
		})
		if err != nil {
			t.Fatal(err)
		}
		id := "register-" + app.Name + "-" + sha
		if err := st.InsertDelivery(ctx, store.Delivery{
			ID: id, AppID: app.ID, EventType: "register",
			CommitSHA: sha, Status: "queued",
		}); err != nil {
			t.Fatalf("delivery %q for app %q: %v", id, name, err)
		}
	}
}

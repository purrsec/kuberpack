package control

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func TestPublishedServicesAreVisibleWithoutBuildsOrWebhooks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(Config{Store: db, APIToken: "test"})
	ctx := context.Background()
	// No forge or builder is configured: published services need neither.
	for _, project := range []string{"pdf", "another"} {
		if err := s.reconcileApps(ctx, project, []promote.ProjectApp{{Name: project + "-app", Image: "example.org/app@sha256:abc", Namespace: project}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []struct {
		method, path string
		status       int
		content      string
	}{
		{"GET", "/api/v1/apps", 200, "pdf-app"},
		{"GET", "/api/v1/apps/pdf-app", 200, "example.org/app@sha256:abc"},
		{"GET", "/api/v1/apps/pdf-app/builds", 200, `"builds":[]`},
		{"POST", "/api/v1/apps/pdf-app/redeploy", 400, "kuberpack project compile"},
	} {
		r := httptest.NewRequest(target.method, target.path, nil)
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != target.status || !strings.Contains(w.Body.String(), target.content) {
			t.Fatalf("%s: %d %s", target.path, w.Code, w.Body.String())
		}
	}
	if err := s.reconcileApps(ctx, "pdf", nil); err != nil {
		t.Fatal(err)
	}
	if apps, err := db.ListImageApps(ctx); err != nil || len(apps) != 1 || apps[0].Project != "another" {
		t.Fatalf("removing one project must preserve the other: %+v %v", apps, err)
	}
	if apps, err := db.ListApps(ctx); err != nil || len(apps) != 0 {
		t.Fatalf("published services must not create repository build targets: %+v %v", apps, err)
	}
}

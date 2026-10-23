package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/promote"
)

func TestInitCreatesALocalManifestBeforeCompileSendsIt(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("KUBERPACK_API_TOKEN", "test-token")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Project, Manifest string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		raw, _ := os.ReadFile("kuberpack.yaml")
		if body.Project != "demo" || body.Manifest != string(raw) {
			t.Error("compile must send the reviewed local source verbatim")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"project":"demo"}`))
	}))
	defer server.Close()
	t.Setenv("KUBERPACK_API_URL", server.URL)
	args := []string{"init", "--project", "demo", "--repository", "owner/app", "--hostname", "demo.example.org"}
	if err := runProject(args); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("kuberpack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promote.ParseProjectManifest(raw); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("init must work offline and must not deploy anything")
	}
	if err := runProject(args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("init must preserve an existing file: %v", err)
	}
	if err := runProject([]string{"compile"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("compile sent %d requests", calls)
	}
}

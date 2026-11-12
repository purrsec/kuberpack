package control

import (
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/promote"
)

// A push on the GitOps repository is the manifest: it must be routed to the
// recompilation, not to the app build.
func TestGitOpsPushIsRoutedToTheProjects(t *testing.T) {
	srv := New(Config{
		GitOpsURL: "https://git.example.org/kuberpack/gitops.git",
		Platform:  promote.Platform{},
	})
	if !srv.isGitOpsRepo("kuberpack/gitops") {
		t.Fatal("the GitOps repository must be recognized")
	}
	if srv.isGitOpsRepo("kuberpack/hello-world") {
		t.Fatal("an app repository must not be taken for the GitOps repository")
	}
}

// The push tells which projects it touched. An unknown push recompiles
// everything: it is the safe answer.
func TestGitOpsProjectsOfAPush(t *testing.T) {
	srv := New(Config{Platform: promote.Platform{ProjectsDir: "kubernetes/vps/projects"}})

	var payload pushPayload
	payload.Commits = []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	}{
		{
			ID:       "a",
			Added:    []string{"kubernetes/vps/projects/demo/kuberpack.yaml"},
			Modified: []string{"kubernetes/vps/projects/demo/site/values.yaml"},
		},
		{
			ID:      "b",
			Removed: []string{"kubernetes/vps/projects/old/kuberpack.yaml"},
		},
	}
	projects := srv.gitOpsProjects(payload)
	if len(projects) != 2 {
		t.Fatalf("projects: %+v", projects)
	}
	found := map[string]bool{}
	for _, p := range projects {
		found[p] = true
	}
	if !found["demo"] || !found["old"] {
		t.Fatalf("projects: %+v", projects)
	}

	// A push that touches nothing of the projects tree recompiles them all.
	empty := pushPayload{}
	empty.Commits = []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	}{
		{ID: "c", Modified: []string{"README.md"}},
	}
	if got := srv.gitOpsProjects(empty); len(got) != 0 {
		t.Fatalf("a push outside the projects must not name a project: %+v", got)
	}
}

// A file of the projects tree belongs to its project.
func TestIsProjectPath(t *testing.T) {
	platform := promote.Platform{ProjectsDir: "kubernetes/vps/projects"}
	cases := map[string]string{
		"kubernetes/vps/projects/demo/kuberpack.yaml":         "demo",
		"kubernetes/vps/projects/demo/site/values.yaml":       "demo",
		"kubernetes/vps/projects/demo/postgres/database.yaml": "demo",
		"kubernetes/vps/apps/site/values.yaml":                "",
		"kubernetes/vps/projects/kustomization.yaml":          "",
		"README.md": "",
	}
	for path, want := range cases {
		got, ok := promote.IsProjectPath(path, platform)
		if want == "" {
			if ok {
				t.Fatalf("%s must not belong to a project", path)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("%s: got %q, want %q", path, got, want)
		}
	}
}

// A push that compiles nothing leaves the GitOps repository untouched, so no
// commit can carry the explanation. The status of the pushed commit is then
// the only place the developer can read why: the reason must be in the
// description, and the state must not be a success.
func TestARefusedProjectExplainsItselfOnTheCommit(t *testing.T) {
	reason := `mitame: namespace "mitame" already exists and is not owned by Kuberpack project "mitame"`
	state, description := compileStatus(promote.ReloadResult{Refused: []string{reason}})
	if state != "failure" {
		t.Fatalf("a refusal must not be published as %q", state)
	}
	if !strings.HasPrefix(description, "mitame: namespace") {
		t.Fatalf("the reason must come first, the description is clipped: %q", description)
	}
	if !strings.Contains(description, reason) {
		t.Fatalf("the description does not carry the reason: %q", description)
	}
}

// Several refusals: the first reason is shown, the rest is counted. The
// description is clipped at 140 runes, so a list of reasons would lose all
// of them.
func TestSeveralRefusalsKeepTheFirstReason(t *testing.T) {
	state, description := compileStatus(promote.ReloadResult{Refused: []string{"first: reason", "second: reason"}})
	if state != "failure" {
		t.Fatalf("state is %q", state)
	}
	if !strings.Contains(description, "first: reason") || !strings.Contains(description, "+1") {
		t.Fatalf("description: %q", description)
	}
}

// A push that compiles everything is a success, and says what it did.
func TestACompiledPushIsASuccess(t *testing.T) {
	state, description := compileStatus(promote.ReloadResult{
		Projects: []promote.ProjectResult{{Project: "demo"}},
		Removed:  []string{"old"},
	})
	if state != "success" {
		t.Fatalf("state is %q", state)
	}
	if description != "1 project(s) compiled, 1 removed" {
		t.Fatalf("description: %q", description)
	}
}

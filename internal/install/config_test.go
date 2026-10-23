package install

import "testing"

func TestSplitApplicationsRepositoryKeepsThePlatformChartOutOfReach(t *testing.T) {
	c := Config{
		Cluster:        "demo",
		Namespace:      "kuberpack",
		BuildNamespace: "kuberpack-build",
		GitProvider:    "forgejo",
		GitOpsURL:      "https://git.example.test/team/platform.git",
		GitOpsBranch:   "main",
		AppsURL:        "https://git.example.test/team/applications.git",
		AppsBranch:     "main",
		Root:           "clusters/demo/kuberpack",
		Source:         "kuberpack-gitops",
		Image:          "example.test/kuberpack:test",
		BuilderImage:   "example.test/builder:test",
	}
	if !c.SplitApps() {
		t.Fatal("a distinct applications URL must be a split installation")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("split configuration must be valid: %v", err)
	}
	if got, want := c.ProjectsPath(), "projects"; got != want {
		t.Fatalf("projects path = %q, want %q", got, want)
	}
	if got, want := c.ChartPath("stateless"), "clusters/demo/kuberpack/charts/stateless"; got != want {
		t.Fatalf("chart path = %q, want %q", got, want)
	}
}

func TestSingleRepositoryKeepsItsHistoricalRoot(t *testing.T) {
	c := Config{
		Cluster:        "demo",
		Namespace:      "kuberpack",
		BuildNamespace: "kuberpack-build",
		GitProvider:    "forgejo",
		GitOpsURL:      "https://git.example.test/team/platform.git",
		GitOpsBranch:   "main",
		AppsURL:        "https://git.example.test/team/platform.git",
		AppsBranch:     "main",
		AppsRoot:       "clusters/demo/kuberpack",
		Root:           "clusters/demo/kuberpack",
		Source:         "kuberpack-gitops",
		Image:          "example.test/kuberpack:test",
		BuilderImage:   "example.test/builder:test",
	}
	if c.SplitApps() {
		t.Fatal("the same repository must keep the historical layout")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("single repository configuration must be valid: %v", err)
	}
	if got, want := c.ProjectsPath(), "clusters/demo/kuberpack/projects"; got != want {
		t.Fatalf("projects path = %q, want %q", got, want)
	}
}

func TestSingleRepositoryCannotUseTwoDifferentRoots(t *testing.T) {
	c := Config{
		Cluster:        "demo",
		Namespace:      "kuberpack",
		BuildNamespace: "kuberpack-build",
		GitProvider:    "forgejo",
		GitOpsURL:      "https://git.example.test/team/platform.git",
		GitOpsBranch:   "main",
		AppsURL:        "https://git.example.test/team/platform.git",
		AppsBranch:     "main",
		AppsRoot:       "apps",
		Root:           "clusters/demo/kuberpack",
		Source:         "kuberpack-gitops",
		Image:          "example.test/kuberpack:test",
		BuilderImage:   "example.test/builder:test",
	}
	if err := c.Validate(); err == nil {
		t.Fatal("a single repository with two roots must be refused")
	}
}

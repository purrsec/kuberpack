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

func TestSingleRepositoryIsRefused(t *testing.T) {
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
	if err := c.Validate(); err == nil {
		t.Fatal("the single-repository layout must be refused")
	}
}

func TestSingleRepositoryWithDifferentRootsIsRefused(t *testing.T) {
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

func TestBuilderPlacementIsOptionalAndValidated(t *testing.T) {
	base := func() Config {
		return Config{
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
	}
	// Le défaut ne change pas : aucune isolation imposée.
	if err := base().Validate(); err != nil {
		t.Fatalf("une installation sans placement doit rester valide: %v", err)
	}
	valid := base()
	valid.BuilderRuntimeClass = "gvisor"
	valid.BuilderNodeSelector = `{"kuberpack.dev/build":"true"}`
	valid.BuilderTolerations = `[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]`
	if err := valid.Validate(); err != nil {
		t.Fatalf("un placement valide est refusé: %v", err)
	}
	cases := []struct {
		name      string
		configure func(*Config)
	}{
		{"RuntimeClass avec espace", func(c *Config) { c.BuilderRuntimeClass = "gvisor sandbox" }},
		{"RuntimeClass avec chemin", func(c *Config) { c.BuilderRuntimeClass = "pools/gvisor" }},
		{"sélecteur qui n'est pas un objet", func(c *Config) { c.BuilderNodeSelector = `["kuberpack.dev/build"]` }},
		{"label de nœud invalide", func(c *Config) { c.BuilderNodeSelector = `{"bad key":"true"}` }},
		{"tolérance qui accepte tous les taints", func(c *Config) {
			c.BuilderTolerations = `[{"operator":"Exists"}]`
		}},
		{"tolérance avec effet inconnu", func(c *Config) {
			c.BuilderTolerations = `[{"key":"dedicated","operator":"Exists","effect":"Sometimes"}]`
		}},
		{"tolérance avec champ inconnu", func(c *Config) {
			c.BuilderTolerations = `[{"key":"dedicated","operator":"Exists","surprise":true}]`
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := base()
			test.configure(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("configuration de placement invalide acceptée")
			}
		})
	}
}

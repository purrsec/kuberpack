package main

import "testing"

// The installation must be doable with two tokens and the URL of the
// GitOps repository: everything else is deduced from it.
func TestTheForgeAndTheRegistryComeFromTheGitOpsURL(t *testing.T) {
	cases := []struct {
		name         string
		repository   string
		forge        string
		user         string
		registry     string
		registryUser string
	}{
		{
			name:         "github",
			repository:   "https://github.com/purrsec/kuberpack-infra",
			forge:        "https://github.com",
			user:         "purrsec",
			registry:     "ghcr.io",
			registryUser: "purrsec",
		},
		{
			name:         "a self hosted forge",
			repository:   "https://git.example.com/team/infra.git",
			forge:        "https://git.example.com",
			user:         "team",
			registry:     "https://git.example.com",
			registryUser: "team",
		},
		{
			name:         "gitlab",
			repository:   "https://gitlab.com/group/subgroup/infra",
			forge:        "https://gitlab.com",
			user:         "group",
			registry:     "https://gitlab.com",
			registryUser: "group",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forgeBaseURL(c.repository); got != c.forge {
				t.Errorf("forgeBaseURL(%q) = %q, want %q", c.repository, got, c.forge)
			}
			if got := forgeUser(c.repository); got != c.user {
				t.Errorf("forgeUser(%q) = %q, want %q", c.repository, got, c.user)
			}
			registry, user := registryOf(c.forge, c.user)
			if registry != c.registry {
				t.Errorf("registryOf(%q) = %q, want %q", c.forge, registry, c.registry)
			}
			if user != c.registryUser {
				t.Errorf("registryOf(%q) user = %q, want %q", c.forge, user, c.registryUser)
			}
		})
	}
}

// A URL that says nothing must not be guessed.
func TestTheForgeOfAnUnreadableURLIsEmpty(t *testing.T) {
	if got := forgeBaseURL(""); got != "" {
		t.Errorf("forgeBaseURL(\"\") = %q, want empty", got)
	}
	if got := forgeUser(""); got != "" {
		t.Errorf("forgeUser(\"\") = %q, want empty", got)
	}
	if got, user := registryOf("", ""); got != "" || user != "" {
		t.Errorf("registryOf(\"\", \"\") = %q, %q, want empty", got, user)
	}
}

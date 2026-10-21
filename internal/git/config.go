package git

import (
	"fmt"
	"strings"
)

// The kinds of forge Kuberpack knows.
const (
	KindForgejo = "forgejo"
	KindGitHub  = "github"
	KindGitLab  = "gitlab"
)

// Config describes the forge Kuberpack talks to.
type Config struct {
	// Kind is forgejo, github or gitlab. Empty means forgejo, the
	// Gitea compatible API.
	Kind string
	// URL is the base URL of the forge. For GitHub it is the API URL
	// (https://api.github.com), for GitLab the API URL
	// (https://gitlab.com/api/v4), for a Forgejo the URL of the instance.
	URL string
	// Token is the token Kuberpack uses to read the repositories and to
	// pose the webhooks.
	Token string
}

// Kinds lists the supported forges.
func Kinds() []string {
	return []string{KindForgejo, KindGitHub, KindGitLab}
}

// New builds the provider of a forge.
func New(cfg Config) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Kind)) {
	case "", KindForgejo, "gitea":
		return NewForgejo(cfg.URL, cfg.Token)
	case KindGitHub:
		return NewGitHub(cfg.URL, cfg.Token)
	case KindGitLab:
		return NewGitLab(cfg.URL, cfg.Token)
	default:
		return nil, fmt.Errorf("unknown git provider %q: known providers are %s",
			cfg.Kind, strings.Join(Kinds(), ", "))
	}
}

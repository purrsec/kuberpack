package release

import (
	"fmt"
	"net/url"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/git"
)

// An application can come from a public third-party forge while Token is the
// platform's GitOps credential. Public clones need no credentials; private
// clones may use only the credentials and origin of their own provider.
func applicationCloneAuth(provider git.Provider, platformToken string, repo git.Repository) ([]string, error) {
	clone, err := url.Parse(repo.CloneURL)
	if err != nil || clone.Host == "" || clone.User != nil || clone.Fragment != "" || (clone.Scheme != "https" && clone.Scheme != "http") {
		return nil, fmt.Errorf("repository clone URL must be an HTTP(S) URL without embedded credentials")
	}
	if !repo.Private {
		return nil, nil
	}
	forge, err := url.Parse(provider.WebURL())
	if err != nil || forge.Scheme != clone.Scheme || !strings.EqualFold(forge.Host, clone.Host) {
		return nil, fmt.Errorf("private repository clone URL must belong to its configured forge")
	}
	token := platformToken
	switch configured := provider.(type) {
	case *git.GitHubProvider:
		token = configured.Token
	case *git.GitLabProvider:
		token = configured.Token
	case *git.ForgejoProvider:
		token = ""
		if configured.Client != nil {
			token = configured.Client.Token
		}
	}
	return provider.CloneAuth(token), nil
}

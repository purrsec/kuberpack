package git

import (
	"fmt"
	"net/url"
	"strings"
)

// Resolve finds who serves a repository.
//
// A repository is written "owner/name": it belongs to the forge of the
// platform, and the token of the platform opens it.
//
// A repository can also be written as its URL: it then lives on the forge of
// that URL. A public repository of another forge is read without a token,
// because the token of the platform means nothing there. This is how a third
// party application is deployed: the code is somewhere else, and Kuberpack
// builds it anyway.
//
// The returned provider, owner and name are what the rest of Kuberpack
// works with.
func Resolve(repo string, platform Provider, platformToken string) (Provider, string, string, error) {
	ref := strings.TrimSpace(repo)
	if ref == "" {
		return nil, "", "", fmt.Errorf("repository is empty")
	}
	if !strings.Contains(ref, "://") {
		owner, name, err := SplitOwnerName(ref)
		if err != nil {
			return nil, "", "", err
		}
		return platform, owner, name, nil
	}
	owner, name, err := RepoOwnerNameFromURL(ref)
	if err != nil {
		return nil, "", "", err
	}
	host := hostOf(ref)
	kind := kindForHost(host)
	if kind == "" {
		// An unknown host: the platform is the one that knows it.
		return platform, owner, name, nil
	}
	if platform != nil && platform.Name() == kind {
		return platform, owner, name, nil
	}
	// Another forge. The token of the platform is not valid there: the
	// public API of the forge is enough to read a public repository.
	provider, err := New(Config{Kind: kind, URL: apiURLFor(host, kind)})
	if err != nil {
		return nil, "", "", err
	}
	return provider, owner, name, nil
}

// kindForHost is the forge a host belongs to.
func kindForHost(host string) string {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "github.com", "www.github.com":
		return KindGitHub
	case "gitlab.com", "www.gitlab.com":
		return KindGitLab
	default:
		return ""
	}
}

// apiURLFor is the API of the forge of a host.
func apiURLFor(host, kind string) string {
	switch kind {
	case KindGitHub:
		return "https://api." + host
	case KindGitLab:
		return "https://" + host + "/api/v4"
	default:
		return ""
	}
}

// hostOf is the host of a URL.
func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

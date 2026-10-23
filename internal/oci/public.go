package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/netguard"
)

// Endpoints are the two addresses a registry needs: the service that
// hands out the tokens, and the API that serves the manifests.
type Endpoints struct {
	// Auth is the service that gives a token for a repository.
	Auth string
	// API is the base URL of the registry API.
	API string
}

// PublicEndpoints are the addresses of the public registries the platform
// knows. A registry that is not listed uses the convention: the token
// service is the registry itself, under /token.
//
// A test replaces the entry of its own host.
var PublicEndpoints = map[string]Endpoints{
	"docker.io": {
		Auth: "https://auth.docker.io/token?service=registry.docker.io&scope=repository:",
		API:  "https://registry-1.docker.io",
	},
	"ghcr.io": {
		Auth: "https://ghcr.io/token?scope=repository:",
		API:  "https://ghcr.io",
	},
	"quay.io": {
		Auth: "https://quay.io/v2/auth?service=quay.io&scope=repository:",
		API:  "https://quay.io",
	},
}

// defaultRegistry is the registry of a reference that does not name one.
const defaultRegistry = "docker.io"

// Pin replaces the tag of a reference with its digest.
//
// The repository is kept as it was written: an image of the default
// registry that is written "nginx:1.27" stays "nginx@sha256:...", and an
// image that is written "docker.io/vaultwarden/server:1.37.4" stays
// "docker.io/vaultwarden/server@sha256:...". A digest alone is not an
// image: the cluster does not know what to pull.
func Pin(reference, digest string) string {
	ref := strings.TrimSpace(reference)
	digest = strings.TrimSpace(digest)
	if ref == "" || digest == "" {
		return ref
	}
	if strings.Contains(ref, "@") {
		// The reference is already pinned.
		return ref
	}
	// The tag is the last colon, when what follows it has no slash: a
	// colon in the host is a port.
	if index := strings.LastIndex(ref, ":"); index >= 0 && !strings.Contains(ref[index+1:], "/") {
		return ref[:index] + "@" + digest
	}
	return ref + "@" + digest
}

// ResolveDigest returns the digest of a tag of a published image.
//
// A reference that already carries a digest is returned unchanged: the
// platform only pins what it can resolve, and never guesses.
//
// The service of the registry is asked for a token, the manifest is read,
// and its digest is returned. Nothing is downloaded.
//
// L'hôte vient du manifeste : il n'est pas de confiance. Les accès sont
// gardés par la politique d'egress de l'environnement.
func ResolveDigest(ctx context.Context, reference string) (string, error) {
	return ResolveDigestWithPolicy(ctx, reference, netguard.FromEnvironment())
}

// ResolveDigestWithPolicy est ResolveDigest avec une politique explicite. Une
// politique nulle retombe sur KUBERPACK_EGRESS_ALLOW, donc sur une garde
// restrictive, jamais sur un client libre.
func ResolveDigestWithPolicy(ctx context.Context, reference string, policy *netguard.Policy) (string, error) {
	host, repository, tag, err := splitReference(reference)
	if err != nil {
		return "", err
	}
	if tag == "" {
		// The reference is already a digest: there is nothing to resolve.
		return reference, nil
	}
	endpoints, ok := PublicEndpoints[host]
	if !ok {
		endpoints = Endpoints{
			Auth: "https://" + host + "/token?scope=repository:",
			API:  "https://" + host,
		}
	}
	if policy == nil {
		policy = netguard.FromEnvironment()
	}
	client := policy.HTTPClient(30 * time.Second)
	token, err := fetchToken(ctx, client, endpoints.Auth, repository)
	if err != nil {
		return "", err
	}
	return manifestDigest(ctx, client, endpoints.API, repository, tag, token)
}

// splitReference splits a reference into its registry, its repository and
// its tag. A reference without a tag is a digest reference.
func splitReference(reference string) (host, repository, tag string, err error) {
	ref := strings.TrimSpace(reference)
	if ref == "" {
		return "", "", "", fmt.Errorf("image reference is empty")
	}
	// The digest comes first: the reference is already pinned.
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		if digest == "" {
			return "", "", "", fmt.Errorf("image reference %q has no digest", reference)
		}
		host, repository, err = splitRepository(repo)
		if err != nil {
			return "", "", "", err
		}
		return host, repository, "", nil
	}
	host, repository, err = splitRepository(ref)
	if err != nil {
		return "", "", "", err
	}
	// The tag is the last colon, after the last slash.
	if index := strings.LastIndex(repository, ":"); index >= 0 {
		tag = repository[index+1:]
		repository = repository[:index]
		if tag == "" {
			return "", "", "", fmt.Errorf("image reference %q has no tag", reference)
		}
	} else {
		tag = "latest"
	}
	return host, repository, tag, nil
}

// splitRepository separates the registry from the repository.
func splitRepository(repository string) (host, name string, err error) {
	repo := strings.TrimSpace(repository)
	if repo == "" {
		return "", "", fmt.Errorf("image reference has no repository")
	}
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 1 || !strings.ContainsAny(parts[0], ".:") {
		// No registry: the image is on the default one.
		if len(parts) == 1 {
			repo = "library/" + repo
		}
		return defaultRegistry, repo, nil
	}
	host = parts[0]
	name = parts[1]
	if host == "index.docker.io" {
		host = defaultRegistry
	}
	if host == defaultRegistry && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if name == "" {
		return "", "", fmt.Errorf("image reference %q has no repository", repository)
	}
	return host, name, nil
}

// fetchToken asks the authentication service for a token to read a
// repository.
func fetchToken(ctx context.Context, client *http.Client, auth, repository string) (string, error) {
	endpoint := auth + url.PathEscape(repository) + ":pull"
	if !strings.Contains(endpoint, "scope=repository:") {
		endpoint = auth + "&scope=repository:" + url.PathEscape(repository) + ":pull"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the registry refused to give a token: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("the registry refused to give a token: %s", res.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("the registry answered something unexpected: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("the registry gave no token")
}

// manifestDigest returns the digest of a manifest.
func manifestDigest(ctx context.Context, client *http.Client, api, repository, tag, token string) (string, error) {
	path := "/v2/" + repository + "/manifests/" + url.PathEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, api+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the registry did not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusMethodNotAllowed {
		// The registry does not answer a HEAD on the manifests: it is
		// read instead.
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, api+path, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", manifestAccept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err = client.Do(req)
		if err != nil {
			return "", fmt.Errorf("the registry did not answer: %w", err)
		}
		defer res.Body.Close()
	}
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("the registry did not give the image %s:%s: %s", repository, tag, res.Status)
	}
	digest := strings.TrimSpace(res.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return "", fmt.Errorf("the registry did not give the digest of %s:%s", repository, tag)
	}
	return digest, nil
}

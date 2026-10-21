package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 1 << 20

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Registry is an OCI Distribution client (Forgejo packages, or any /v2 registry).
type Registry struct {
	BaseURL    string
	User       string
	Token      string
	HTTPClient *http.Client
}

func New(baseURL, user, token string) (*Registry, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("registry URL is empty")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("registry URL: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("registry token is empty")
	}
	return &Registry{
		BaseURL:    baseURL,
		User:       strings.TrimSpace(user),
		Token:      strings.TrimSpace(token),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ManifestDigest returns Docker-Content-Digest for repository:tag.
func (r *Registry) ManifestDigest(ctx context.Context, repository, tag string) (string, error) {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	tag = strings.TrimSpace(tag)
	if repository == "" || tag == "" {
		return "", fmt.Errorf("repository and tag are required")
	}
	path := "/v2/" + repository + "/manifests/" + url.PathEscape(tag)
	res, raw, err := r.do(ctx, http.MethodHead, path, manifestAccept)
	if err != nil {
		return "", err
	}
	if res.StatusCode == http.StatusMethodNotAllowed || res.StatusCode == http.StatusNotFound {
		res, raw, err = r.do(ctx, http.MethodGet, path, manifestAccept)
		if err != nil {
			return "", err
		}
	}
	if res.StatusCode == http.StatusNotFound {
		return "", statusError{http.StatusNotFound, fmt.Sprintf("%s:%s not in registry", repository, tag)}
	}
	if res.StatusCode != http.StatusOK {
		return "", statusError{res.StatusCode, fmt.Sprintf("registry %s %s", res.Status, hintBody(raw))}
	}
	digest := strings.ToLower(strings.TrimSpace(res.Header.Get("Docker-Content-Digest")))
	if digest == "" {
		return "", fmt.Errorf("registry omitted Docker-Content-Digest for %s:%s", repository, tag)
	}
	return digest, nil
}

// Tags lists tags for an OCI repository.
func (r *Registry) Tags(ctx context.Context, repository string) ([]string, error) {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if repository == "" {
		return nil, fmt.Errorf("repository is empty")
	}
	path := "/v2/" + repository + "/tags/list"
	res, raw, err := r.do(ctx, http.MethodGet, path, "application/json")
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		return nil, statusError{http.StatusNotFound, repository + " has no tags"}
	}
	if res.StatusCode != http.StatusOK {
		return nil, statusError{res.StatusCode, fmt.Sprintf("registry tags %s %s", res.Status, hintBody(raw))}
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode tags list: %w", err)
	}
	return body.Tags, nil
}

func (r *Registry) do(ctx context.Context, method, path, accept string) (*http.Response, []byte, error) {
	res, raw, err := r.request(ctx, method, path, accept, r.basicAuth())
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode != http.StatusUnauthorized {
		return res, raw, nil
	}
	// A hosted registry (GHCR, ECR, GAR) refuses the credentials and asks
	// for a token. The exchange is the standard flow of the registry API.
	challenge, err := parseChallenge(res.Header.Get("Www-Authenticate"))
	if err != nil {
		return res, raw, nil
	}
	token, err := r.exchange(ctx, challenge)
	if err != nil {
		return res, raw, nil
	}
	return r.request(ctx, method, path, accept, "Bearer "+token)
}

// basicAuth is the header of a registry that accepts the credentials of the
// platform directly.
func (r *Registry) basicAuth() string {
	user := r.User
	if user == "" {
		user = "token"
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+r.Token))
}

// request does one call to the registry with the given authorization.
func (r *Registry) request(ctx context.Context, method, path, accept, authorization string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", authorization)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxBody {
		return nil, nil, fmt.Errorf("registry response too large")
	}
	return res, raw, nil
}

// statusError carries the HTTP status of a registry answer so callers can
// tell "not published yet" (404) from "rejected" (401/403) from a hiccup.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

// IsNotFound reports whether the registry answered 404.
func IsNotFound(err error) bool {
	var se statusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

// IsTransient reports whether the failure is worth retrying: a 404 (the tag
// is not published yet), a server error or a transport hiccup. An explicit
// rejection (401, 403, 400) is final.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var se statusError
	if errors.As(err, &se) {
		switch se.code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest:
			return false
		}
		return true
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

func hintBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return ": " + s
}

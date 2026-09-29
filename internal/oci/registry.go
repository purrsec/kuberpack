package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
		return "", notFoundError{fmt.Sprintf("%s:%s not in registry", repository, tag)}
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s %s", res.Status, hintBody(raw))
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
		return nil, notFoundError{repository + " has no tags"}
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry tags %s %s", res.Status, hintBody(raw))
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
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", accept)
	user := r.User
	if user == "" {
		user = "token"
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+r.Token)))

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

type notFoundError struct {
	msg string
}

func (e notFoundError) Error() string { return e.msg }

func IsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
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

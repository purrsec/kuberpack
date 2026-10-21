package git

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitLabProvider talks to the GitLab API v4.
//
// It also serves a self hosted GitLab: the API URL is configurable, and the
// clone URL of a repository comes from the API, so the instance is used as
// it is.
type GitLabProvider struct {
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

// NewGitLab builds the provider of a GitLab instance. The API URL defaults
// to the public one; a self hosted instance passes its own
// (https://gitlab.example.org/api/v4).
func NewGitLab(apiURL, token string) (*GitLabProvider, error) {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		apiURL = "https://gitlab.com/api/v4"
	}
	if _, err := url.Parse(apiURL); err != nil {
		return nil, fmt.Errorf("GitLab API URL: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("GitLab token is empty")
	}
	return &GitLabProvider{
		APIURL:     apiURL,
		Token:      strings.TrimSpace(token),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *GitLabProvider) Name() string { return "gitlab" }

func (p *GitLabProvider) Repository(ctx context.Context, owner, name string) (Repository, error) {
	var raw struct {
		PathWithNamespace string `json:"path_with_namespace"`
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/projects/"+projectID(owner, name), nil, &raw); err != nil {
		return Repository{}, err
	}
	full := raw.PathWithNamespace
	if full == "" {
		full = owner + "/" + name
	}
	return Repository{
		Owner:         owner,
		Name:          name,
		FullName:      full,
		CloneURL:      raw.HTTPURLToRepo,
		DefaultBranch: raw.DefaultBranch,
		Private:       raw.Visibility == "private",
	}, nil
}

func (p *GitLabProvider) BranchSHA(ctx context.Context, owner, name, branch string) (string, error) {
	var raw struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	path := "/projects/" + projectID(owner, name) + "/repository/branches/" + url.PathEscape(branch)
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return "", err
	}
	if raw.Commit.ID == "" {
		return "", fmt.Errorf("branch %s of %s/%s has no commit", branch, owner, name)
	}
	return raw.Commit.ID, nil
}

func (p *GitLabProvider) EnsureHook(ctx context.Context, owner, name, hookURL, secret string) (Hook, error) {
	base := "/projects/" + projectID(owner, name) + "/hooks"
	var hooks []struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := p.doJSON(ctx, http.MethodGet, base, nil, &hooks); err != nil {
		return Hook{}, err
	}
	body := map[string]any{
		"url":                     hookURL,
		"token":                   secret,
		"push_events":             true,
		"merge_requests_events":   true,
		"enable_ssl_verification": true,
	}
	for _, hook := range hooks {
		if hook.URL != hookURL {
			continue
		}
		if err := p.doJSON(ctx, http.MethodPut, fmt.Sprintf("%s/%d", base, hook.ID), body, nil); err != nil {
			return Hook{}, err
		}
		return Hook{ID: hook.ID, URL: hookURL}, nil
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := p.doJSON(ctx, http.MethodPost, base, body, &created); err != nil {
		return Hook{}, err
	}
	return Hook{ID: created.ID, URL: hookURL}, nil
}

func (p *GitLabProvider) DeleteHook(ctx context.Context, owner, name string, id int64) error {
	path := fmt.Sprintf("/projects/%s/hooks/%d", projectID(owner, name), id)
	return p.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (p *GitLabProvider) CommitStatus(ctx context.Context, owner, name, sha string, status Status) error {
	if strings.TrimSpace(sha) == "" {
		return fmt.Errorf("commit SHA is empty")
	}
	if strings.TrimSpace(status.State) == "" {
		return fmt.Errorf("commit status state is empty")
	}
	if status.Context == "" {
		status.Context = ProductionContext
	}
	if len(status.Description) > 140 {
		status.Description = status.Description[:140]
	}
	body := map[string]any{
		"state":       status.State,
		"name":        status.Context,
		"description": status.Description,
		"target_url":  status.TargetURL,
	}
	path := fmt.Sprintf("/projects/%s/statuses/%s", projectID(owner, name), url.PathEscape(sha))
	return p.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (p *GitLabProvider) RawFile(ctx context.Context, owner, name, path, ref string) (string, error) {
	api := "/projects/" + projectID(owner, name) + "/repository/files/" + url.PathEscape(strings.TrimPrefix(path, "/"))
	if ref != "" {
		api += "?ref=" + url.QueryEscape(ref)
	}
	var raw struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := p.doJSON(ctx, http.MethodGet, api, nil, &raw); err != nil {
		return "", err
	}
	if raw.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(raw.Content)
		if err != nil {
			return "", fmt.Errorf("decode %s: %w", path, err)
		}
		return string(decoded), nil
	}
	return raw.Content, nil
}

func (p *GitLabProvider) ResolveCommit(ctx context.Context, owner, name, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is empty")
	}
	var raw struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/projects/%s/repository/commits/%s", projectID(owner, name), url.PathEscape(ref))
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return "", err
	}
	if raw.ID == "" {
		return "", fmt.Errorf("ref %q of %s/%s has no commit", ref, owner, name)
	}
	return raw.ID, nil
}

func (p *GitLabProvider) WebURL() string {
	return strings.TrimSuffix(p.APIURL, "/api/v4")
}

// VerifyWebhook checks the X-Gitlab-Token header of a delivery. GitLab
// sends the secret as is, it does not sign the body.
func (p *GitLabProvider) VerifyWebhook(secret string, header http.Header, body []byte) bool {
	token := strings.TrimSpace(header.Get("X-Gitlab-Token"))
	if token == "" || secret == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(secret))
}

// Event returns the event type of a delivery.
func (p *GitLabProvider) Event(header http.Header) string {
	event := strings.TrimSpace(header.Get("X-Gitlab-Event"))
	switch strings.ToLower(event) {
	case "push hook":
		return "push"
	case "merge request hook":
		return "pull_request"
	case "":
		return "push"
	default:
		return strings.ToLower(event)
	}
}

func (p *GitLabProvider) doJSON(ctx context.Context, method, path string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.APIURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", p.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("gitlab %s %s: %s: %s", method, path, resp.Status, hint(raw))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(raw, dest)
}

// projectID is the identifier of a project in the GitLab API: the path of
// the project, URL encoded.
func projectID(owner, name string) string {
	return url.PathEscape(owner + "/" + name)
}

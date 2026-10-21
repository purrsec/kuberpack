package git

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubProvider talks to the GitHub REST API.
//
// It also serves GitHub Enterprise: the API URL is configurable, and the
// clone URL of a repository comes from the API, so the instance is used as
// it is.
type GitHubProvider struct {
	APIURL string
	Token  string
	// WebURL is where a human opens the forge. It is the API URL of a
	// GitHub Enterprise instance minus its /api/v3 suffix.
	WebURLBase string
	HTTPClient *http.Client
}

// NewGitHub builds the provider of a GitHub instance. The API URL defaults
// to the public one; a GitHub Enterprise instance passes its own
// (https://ghe.example.org/api/v3).
func NewGitHub(apiURL, token string) (*GitHubProvider, error) {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	if _, err := url.Parse(apiURL); err != nil {
		return nil, fmt.Errorf("GitHub API URL: %w", err)
	}
	// An empty token means an anonymous access: enough to read a public
	// repository of a forge the platform has no token for.
	_ = strings.TrimSpace(token)
	web := strings.TrimSuffix(apiURL, "/api/v3")
	if web == apiURL {
		web = "https://github.com"
	}
	return &GitHubProvider{
		APIURL:     apiURL,
		Token:      strings.TrimSpace(token),
		WebURLBase: web,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *GitHubProvider) Name() string { return "github" }

func (p *GitHubProvider) Repository(ctx context.Context, owner, name string) (Repository, error) {
	var raw struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		Name          string `json:"name"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return Repository{}, err
	}
	full := raw.FullName
	if full == "" {
		full = owner + "/" + raw.Name
	}
	return Repository{
		Owner:         raw.Owner.Login,
		Name:          raw.Name,
		FullName:      full,
		CloneURL:      raw.CloneURL,
		DefaultBranch: raw.DefaultBranch,
		Private:       raw.Private,
	}, nil
}

func (p *GitHubProvider) BranchSHA(ctx context.Context, owner, name, branch string) (string, error) {
	var raw struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/branches/" + url.PathEscape(branch)
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return "", err
	}
	if raw.Commit.SHA == "" {
		return "", fmt.Errorf("branch %s of %s/%s has no commit", branch, owner, name)
	}
	return raw.Commit.SHA, nil
}

func (p *GitHubProvider) EnsureHook(ctx context.Context, owner, name, hookURL, secret string) (Hook, error) {
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/hooks"
	var hooks []struct {
		ID     int64 `json:"id"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := p.doJSON(ctx, http.MethodGet, base, nil, &hooks); err != nil {
		return Hook{}, err
	}
	body := map[string]any{
		"name":   "web",
		"active": true,
		"events": []string{"push", "pull_request"},
		"config": map[string]any{
			"url":          hookURL,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	}
	for _, hook := range hooks {
		if hook.Config.URL != hookURL {
			continue
		}
		if err := p.doJSON(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", base, hook.ID), body, nil); err != nil {
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

func (p *GitHubProvider) DeleteHook(ctx context.Context, owner, name string, id int64) error {
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(name), id)
	return p.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (p *GitHubProvider) CommitStatus(ctx context.Context, owner, name, sha string, status Status) error {
	if strings.TrimSpace(sha) == "" {
		return fmt.Errorf("commit SHA is empty")
	}
	if strings.TrimSpace(status.State) == "" {
		return fmt.Errorf("commit status state is empty")
	}
	if status.Context == "" {
		status.Context = "kuberpack/production"
	}
	if len(status.Description) > 140 {
		status.Description = status.Description[:140]
	}
	body := map[string]any{
		"state":       status.State,
		"context":     status.Context,
		"description": status.Description,
		"target_url":  status.TargetURL,
	}
	path := fmt.Sprintf("/repos/%s/%s/statuses/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(sha))
	return p.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (p *GitHubProvider) RawFile(ctx context.Context, owner, name, path, ref string) (string, error) {
	api := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/" + strings.TrimPrefix(path, "/")
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
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw.Content, "\n", ""))
		if err != nil {
			return "", fmt.Errorf("decode %s: %w", path, err)
		}
		return string(decoded), nil
	}
	return raw.Content, nil
}

func (p *GitHubProvider) ResolveCommit(ctx context.Context, owner, name, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is empty")
	}
	var raw struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return "", err
	}
	if raw.SHA == "" {
		return "", fmt.Errorf("ref %q of %s/%s has no commit", ref, owner, name)
	}
	return raw.SHA, nil
}

func (p *GitHubProvider) WebURL() string {
	return p.WebURLBase
}

func (p *GitHubProvider) CurrentUser(ctx context.Context) (string, error) {
	var raw struct {
		Login string `json:"login"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/user", nil, &raw); err != nil {
		return "", err
	}
	if raw.Login == "" {
		return "", fmt.Errorf("github: the token has no user")
	}
	return raw.Login, nil
}

// VerifyWebhook checks the X-Hub-Signature-256 header of a delivery.
func (p *GitHubProvider) VerifyWebhook(secret string, header http.Header, body []byte) bool {
	signature := strings.TrimSpace(header.Get("X-Hub-Signature-256"))
	if signature == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// Event returns the event type of a delivery.
func (p *GitHubProvider) Event(header http.Header) string {
	event := strings.TrimSpace(header.Get("X-GitHub-Event"))
	if event == "" {
		return "push"
	}
	return event
}

func (p *GitHubProvider) doJSON(ctx context.Context, method, path string, body any, dest any) error {
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
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
		return fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, hint(raw))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(raw, dest)
}

// hint returns the readable part of an error payload.
func hint(raw []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Message != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(raw))
}

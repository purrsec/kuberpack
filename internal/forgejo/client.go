package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 1 << 20

// Client talks to the Forgejo (Gitea) HTTP API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type Repo struct {
	Owner    string
	Name     string
	FullName string
	CloneURL string
	SSHURL   string
	Private  bool
}

func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("Forgejo URL is empty")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("Forgejo URL: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Forgejo token is empty")
	}
	return &Client{
		BaseURL:    baseURL,
		Token:      strings.TrimSpace(token),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Client) Repo(ctx context.Context, owner, name string) (Repo, error) {
	var raw struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
		Private  bool   `json:"private"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := c.get(ctx, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), &raw); err != nil {
		return Repo{}, err
	}
	repo := Repo{
		Owner:    raw.Owner.Login,
		Name:     raw.Name,
		FullName: raw.FullName,
		CloneURL: raw.CloneURL,
		SSHURL:   raw.SSHURL,
		Private:  raw.Private,
	}
	if repo.Owner == "" {
		repo.Owner = owner
	}
	if repo.Name == "" {
		repo.Name = name
	}
	return repo, nil
}

func (c *Client) BranchSHA(ctx context.Context, owner, name, branch string) (string, error) {
	var raw struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	path := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/branches/" + url.PathEscape(branch)
	if err := c.get(ctx, path, &raw); err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(raw.Commit.ID))
	if sha == "" {
		return "", fmt.Errorf("branch %s has no commit SHA", branch)
	}
	return sha, nil
}

func (c *Client) DispatchWorkflow(ctx context.Context, owner, repo, workflow, ref string, inputs map[string]string) error {
	if ref == "" {
		ref = "main"
	}
	body, err := json.Marshal(map[string]any{
		"ref":    ref,
		"inputs": inputs,
	})
	if err != nil {
		return err
	}
	path := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/workflows/" + url.PathEscape(workflow) + "/dispatches"
	return c.do(ctx, http.MethodPost, path, body, http.StatusNoContent, http.StatusCreated, http.StatusOK)
}

func (c *Client) get(ctx context.Context, path string, dest any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, dest, http.StatusOK)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, dest any, want ...int) error {
	raw, err := c.doRaw(ctx, method, path, body, want...)
	if err != nil {
		return err
	}
	if dest == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode Forgejo response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, want ...int) error {
	_, err := c.doRaw(ctx, method, path, body, want...)
	return err
}

func (c *Client) doRaw(ctx context.Context, method, path string, body []byte, want ...int) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("Forgejo response too large")
	}

	ok := false
	for _, code := range want {
		if res.StatusCode == code {
			ok = true
			break
		}
	}
	if ok {
		return raw, nil
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("Forgejo rejected the token (%s)%s", res.Status, hintBody(raw))
	case http.StatusNotFound:
		return nil, fmt.Errorf("Forgejo repository not found%s", hintBody(raw))
	default:
		return nil, fmt.Errorf("Forgejo API %s%s", res.Status, hintBody(raw))
	}
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

func ParseOwnerName(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repository must be owner/name")
	}
	return owner, name, nil
}

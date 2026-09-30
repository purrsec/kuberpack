package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) RawFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	path = strings.TrimPrefix(path, "/")
	if owner == "" || repo == "" || path == "" {
		return nil, fmt.Errorf("repository path is empty")
	}
	api := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/raw/" + path
	if ref != "" {
		api += "?ref=" + url.QueryEscape(ref)
	}
	return c.doRaw(ctx, http.MethodGet, api, nil, http.StatusOK)
}

func (c *Client) ResolveCommit(ctx context.Context, owner, repo, ref string) (string, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return "", fmt.Errorf("commit ref is empty")
	}
	if len(ref) == 40 && trackHex(ref) {
		return ref, nil
	}
	var raw struct {
		SHA string `json:"sha"`
	}
	api := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/commits/" + url.PathEscape(ref)
	if err := c.get(ctx, api, &raw); err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(raw.SHA))
	if sha == "" {
		return "", fmt.Errorf("commit %s has no SHA", ref)
	}
	return sha, nil
}

func RepoOwnerNameFromURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("git URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("git URL: %w", err)
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	return ParseOwnerName(path)
}

func trackHex(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return false
	}
	return s != ""
}

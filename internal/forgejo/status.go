package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"

	ProductionContext = "kuberpack/production"
	maxStatusDesc     = 140
)

type CommitStatus struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context,omitempty"`
}

func TruncateStatus(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxStatusDesc {
		return s
	}
	return string(runes[:maxStatusDesc-1]) + "…"
}

func (c *Client) CreateCommitStatus(ctx context.Context, owner, repo, sha string, status CommitStatus) error {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return fmt.Errorf("commit SHA is empty")
	}
	if strings.TrimSpace(status.State) == "" {
		return fmt.Errorf("commit status state is empty")
	}
	if status.Context == "" {
		status.Context = ProductionContext
	}
	status.Description = TruncateStatus(status.Description)
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	path := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/statuses/" + url.PathEscape(sha)
	return c.doJSON(ctx, http.MethodPost, path, body, nil, http.StatusCreated, http.StatusOK)
}

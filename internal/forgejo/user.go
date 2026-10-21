package forgejo

import (
	"context"
	"fmt"
	"net/http"
)

// CurrentUser returns the account the token belongs to.
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var raw struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, "/api/v1/user", &raw); err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	if raw.Login == "" {
		return "", fmt.Errorf("current user: the token has no user")
	}
	return raw.Login, nil
}

var _ = http.StatusOK

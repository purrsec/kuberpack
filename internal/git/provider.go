// Package git is the abstraction over the git providers Kuberpack talks to.
//
// Kuberpack is not a client of one forge: it reads a repository, follows a
// branch, poses a webhook and publishes a status. A provider is whatever
// knows how to do that. Forgejo, GitHub and GitLab are providers; so is
// anything else that speaks one of their APIs.
package git

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Repository is what Kuberpack needs to know about a repository.
type Repository struct {
	// Owner is the account that holds the repository.
	Owner string
	// Name is the name of the repository.
	Name string
	// FullName is "owner/name".
	FullName string
	// CloneURL is the HTTPS URL the builder clones.
	CloneURL string
	// DefaultBranch is the branch a push is expected on.
	DefaultBranch string
	// Private tells whether the repository needs a token to be cloned.
	Private bool
}

// Hook is a webhook of a repository.
type Hook struct {
	ID  int64
	URL string
}

// Status is a commit status, the state a forge shows next to a commit.
type Status struct {
	// State is pending, success, failure or warning.
	State string
	// Context names the producer of the status.
	Context string
	// Description is the short text next to the state.
	Description string
	// TargetURL is where the state leads.
	TargetURL string
}

// The contexts of the commit statuses.
const (
	// ProductionContext is the context of the status of a production build.
	ProductionContext = "kuberpack/production"
	// PreviewContext is the context of the status of a preview build.
	PreviewContext = "kuberpack/preview"
)

// RepoOwnerNameFromURL extracts the owner and the name of a repository from
// its clone URL. It works with every forge, because it only reads the path.
func RepoOwnerNameFromURL(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("repository URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("repository URL %q: %w", raw, err)
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("repository URL %q must end with owner/name", raw)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

// The states a commit status can take.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusWarning = "warning"
)

// Provider is what Kuberpack expects from a git forge.
type Provider interface {
	// Name identifies the provider.
	Name() string
	// Repository returns the metadata of a repository.
	Repository(ctx context.Context, owner, name string) (Repository, error)
	// BranchSHA returns the head commit of a branch.
	BranchSHA(ctx context.Context, owner, name, branch string) (string, error)
	// EnsureHook creates the webhook of a repository, or updates it when it
	// already exists.
	EnsureHook(ctx context.Context, owner, name, hookURL, secret string) (Hook, error)
	// DeleteHook removes the webhook of a repository.
	DeleteHook(ctx context.Context, owner, name string, id int64) error
	// CommitStatus publishes a status on a commit.
	CommitStatus(ctx context.Context, owner, name, sha string, status Status) error
	// RawFile reads a file of a repository, at a ref.
	RawFile(ctx context.Context, owner, name, path, ref string) (string, error)
	// ResolveCommit turns a ref (a branch, a tag, a SHA) into a commit SHA.
	ResolveCommit(ctx context.Context, owner, name, ref string) (string, error)
	// WebURL is the base URL of the forge, the one a human opens in a
	// browser. The links of the commit statuses lead there.
	WebURL() string
	// CloneAuth is the git arguments that authenticate a clone of one of
	// its repositories. Every forge has its own way: a token in a header
	// for a Gitea, the basic scheme for GitHub and GitLab.
	CloneAuth(token string) []string
	// CurrentUser is the account the token belongs to. The images are
	// published under that namespace: the code may live in the repository
	// of somebody else.
	CurrentUser(ctx context.Context) (string, error)
	// VerifyWebhook checks the signature of a delivery.
	VerifyWebhook(secret string, header http.Header, body []byte) bool
	// Event returns the event type of a delivery ("push", "pull_request").
	Event(header http.Header) string
}

// SplitOwnerName splits an "owner/name" reference.
func SplitOwnerName(full string) (string, string, error) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", "", fmt.Errorf("repository is empty")
	}
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repository %q must be owner/name", full)
	}
	return parts[0], parts[1], nil
}

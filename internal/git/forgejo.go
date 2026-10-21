package git

import (
	"context"
	"net/http"

	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
)

// ForgejoProvider talks to a Forgejo (or Gitea) instance.
type ForgejoProvider struct {
	Client *forgejo.Client
}

// NewForgejo builds the provider of a Forgejo instance.
func NewForgejo(baseURL, token string) (*ForgejoProvider, error) {
	client, err := forgejo.New(baseURL, token)
	if err != nil {
		return nil, err
	}
	return &ForgejoProvider{Client: client}, nil
}

func (p *ForgejoProvider) Name() string { return "forgejo" }

func (p *ForgejoProvider) Repository(ctx context.Context, owner, name string) (Repository, error) {
	repo, err := p.Client.Repo(ctx, owner, name)
	if err != nil {
		return Repository{}, err
	}
	return Repository{
		Owner:         repo.Owner,
		Name:          repo.Name,
		FullName:      repo.FullName,
		CloneURL:      repo.CloneURL,
		DefaultBranch: repo.DefaultBranch,
		Private:       repo.Private,
	}, nil
}

func (p *ForgejoProvider) BranchSHA(ctx context.Context, owner, name, branch string) (string, error) {
	return p.Client.BranchSHA(ctx, owner, name, branch)
}

func (p *ForgejoProvider) EnsureHook(ctx context.Context, owner, name, hookURL, secret string) (Hook, error) {
	hook, err := p.Client.EnsureHook(ctx, owner, name, hookURL, secret)
	if err != nil {
		return Hook{}, err
	}
	return Hook{ID: hook.ID, URL: hookURL}, nil
}

func (p *ForgejoProvider) DeleteHook(ctx context.Context, owner, name string, id int64) error {
	return p.Client.DeleteHook(ctx, owner, name, id)
}

func (p *ForgejoProvider) CommitStatus(ctx context.Context, owner, name, sha string, status Status) error {
	return p.Client.CreateCommitStatus(ctx, owner, name, sha, forgejo.CommitStatus{
		State:       status.State,
		TargetURL:   status.TargetURL,
		Description: status.Description,
		Context:     status.Context,
	})
}

func (p *ForgejoProvider) RawFile(ctx context.Context, owner, name, path, ref string) (string, error) {
	raw, err := p.Client.RawFile(ctx, owner, name, path, ref)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (p *ForgejoProvider) ResolveCommit(ctx context.Context, owner, name, ref string) (string, error) {
	return p.Client.ResolveCommit(ctx, owner, name, ref)
}

func (p *ForgejoProvider) WebURL() string {
	if p.Client == nil {
		return ""
	}
	return p.Client.BaseURL
}

func (p *ForgejoProvider) CloneAuth(token string) []string {
	return fetch.TokenHeaderArgs(token)
}

func (p *ForgejoProvider) CurrentUser(ctx context.Context) (string, error) {
	return p.Client.CurrentUser(ctx)
}

func (p *ForgejoProvider) VerifyWebhook(secret string, header http.Header, body []byte) bool {
	return hmacsig.Valid(secret, hmacsig.Header(header), body)
}

func (p *ForgejoProvider) Event(header http.Header) string {
	return hmacsig.Event(header)
}

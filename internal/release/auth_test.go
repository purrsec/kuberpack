package release

import (
	"context"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/git"
)

// fakeProvider is a forge that answers without a network.
type fakeProvider struct {
	git.Provider
	repo git.Repository
}

func (f fakeProvider) Name() string { return git.KindGitHub }

func (f fakeProvider) Repository(ctx context.Context, owner, name string) (git.Repository, error) {
	return f.repo, nil
}

func (f fakeProvider) CloneAuth(token string) []string {
	return git.CloneAuthArgs(git.AuthScheme(f.Name()), token)
}

func (f fakeProvider) WebURL() string { return "https://github.com" }

// A GitHub repository refuses the token of a Gitea: the forge answers
// "invalid credentials", the retry without credentials asks for a username
// on a terminal nobody watches, and the build dies on a clone that was
// always right.
func TestTheCloneOfTheApplicationFollowsItsForge(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{git.KindGitHub, "Authorization: Basic "},
		{git.KindGitLab, "Authorization: Basic "},
		{git.KindForgejo, "Authorization: token "},
	}
	for _, tc := range cases {
		args := git.CloneAuthArgs(git.AuthScheme(tc.provider), "s3cret")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, tc.want) {
			t.Errorf("%s: the clone of the application uses %q, want %q", tc.provider, joined, tc.want)
		}
	}
}

// The GitOps repository lives on the forge of the platform, which is not
// necessarily the forge of the application.
func TestTheGitOpsCloneFollowsThePlatform(t *testing.T) {
	req := Request{GitOpsAuthArgs: []string{"-c", "http.extraHeader=Authorization: Basic x"}}
	got := strings.Join(req.gitOpsAuthArgs(), " ")
	if !strings.Contains(got, "Basic") {
		t.Errorf("the GitOps clone uses %q, want the arguments of the platform", got)
	}
	req.GitOpsAuthArgs = nil
	req.Token = "s3cret"
	req.Provider = fakeProvider{}
	if got := strings.Join(req.gitOpsAuthArgs(), " "); !strings.Contains(got, "Basic") {
		t.Errorf("the GitOps clone uses %q, want the scheme of the platform", got)
	}
}

// The clone of the application is made with the scheme of its own forge.
func TestTheApplicationCloneIsNotTheGiteaOne(t *testing.T) {
	var seen []string
	previous := checkout
	checkout = func(ctx context.Context, cloneURL, commitSHA, dest string, extraGitArgs []string) error {
		seen = extraGitArgs
		return nil
	}
	defer func() { checkout = previous }()

	provider := fakeProvider{repo: git.Repository{
		Owner:    "o",
		Name:     "n",
		FullName: "o/n",
		CloneURL: "https://github.com/o/n.git",
		Private:  true,
	}}
	req := Request{Provider: provider, Token: "s3cret", Owner: "o", Name: "n", SHA: strings.Repeat("a", 40)}
	// The release fails after the clone (there is no source to analyze):
	// the arguments of the clone are what matters here.
	_, _ = Run(context.Background(), req)
	if len(seen) == 0 {
		t.Fatal("the clone of the application was never made")
	}
	if joined := strings.Join(seen, " "); !strings.Contains(joined, "Authorization: Basic") {
		t.Errorf("the clone of the application uses %q, want a basic authentication", joined)
	}
}

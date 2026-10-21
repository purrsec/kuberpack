package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

// gitOpsSecretID is the key of the HMAC secret of the GitOps webhook. It is
// not an app: the webhook of the repository that holds the manifests.
const gitOpsSecretID int64 = 0

// gitOpsRepo returns the owner and the name of the GitOps repository.
func (s *Server) gitOpsRepo() (string, string, bool) {
	if s.cfg.GitOpsURL == "" {
		return "", "", false
	}
	owner, repo, err := git.RepoOwnerNameFromURL(s.cfg.GitOpsURL)
	if err != nil {
		return "", "", false
	}
	return owner, repo, true
}

// isGitOpsRepo reports whether a push comes from the repository that holds
// the manifests.
func (s *Server) isGitOpsRepo(fullName string) bool {
	owner, repo, ok := s.gitOpsRepo()
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fullName), owner+"/"+repo)
}

// ensureGitOpsHook makes sure the GitOps repository notifies Kuberpack of the
// pushes that change a manifest. Without it, a manifest edited in Git stays
// inert until it is posted to the API.
func (s *Server) ensureGitOpsHook(ctx context.Context) error {
	owner, repo, ok := s.gitOpsRepo()
	if !ok {
		return nil
	}
	if s.cfg.WebhookURL == "" || s.cfg.Provider == nil {
		return nil
	}
	secret, err := s.cfg.Secrets.Get(gitOpsSecretID)
	if err != nil || strings.TrimSpace(secret) == "" {
		secret, err = hmacsig.NewSecret()
		if err != nil {
			return err
		}
		if err := s.cfg.Secrets.Put(gitOpsSecretID, secret); err != nil {
			return err
		}
	}
	hook, err := s.cfg.Provider.EnsureHook(ctx, owner, repo, s.cfg.WebhookURL, secret)
	if err != nil {
		return err
	}
	log.Printf("kuberpack gitops webhook %s/%s hook=%d", owner, repo, hook.ID)
	return nil
}

// handleGitOpsWebhook validates the signature of a push on the GitOps
// repository and recompiles the projects it touched.
//
// The signature is checked with the secret of the GitOps webhook, not with
// the secret of an app: the repository holds the manifests, it is not an
// application.
func (s *Server) handleGitOpsWebhook(w http.ResponseWriter, r *http.Request, payload pushPayload, body []byte) {
	secret, err := s.gitOpsSecret()
	if err != nil {
		http.Error(w, "webhook secret missing", http.StatusInternalServerError)
		return
	}
	if !s.cfg.Provider.VerifyWebhook(secret, r.Header, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	event := s.cfg.Provider.Event(r.Header)
	if event != "" && event != "push" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A branch deletion is not a manifest change.
	if payload.Deleted {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.handleGitOpsPush(r.Context(), payload); err != nil {
		// The error is published as a commit status: the developer sees it
		// on the commit, not in a log.
		log.Printf("kuberpack gitops compile: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// gitOpsSecret returns the HMAC secret of the GitOps webhook, creating it on
// first use.
func (s *Server) gitOpsSecret() (string, error) {
	secret, err := s.cfg.Secrets.Get(gitOpsSecretID)
	if err == nil && strings.TrimSpace(secret) != "" {
		return secret, nil
	}
	secret, err = hmacsig.NewSecret()
	if err != nil {
		return "", err
	}
	if err := s.cfg.Secrets.Put(gitOpsSecretID, secret); err != nil {
		return "", err
	}
	return secret, nil
}

// handleGitOpsPush recompiles the projects whose manifest changed in the
// GitOps repository.
//
// The manifest is the source of truth: it is read from the repository and
// never rewritten. A project whose manifest disappeared is deleted.
func (s *Server) handleGitOpsPush(ctx context.Context, payload pushPayload) error {
	owner, repo, ok := s.gitOpsRepo()
	if !ok {
		return errors.New("gitops is not configured")
	}
	projects := s.gitOpsProjects(payload)

	result, err := promote.ReloadProjects(ctx, promote.ReloadRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		Projects:     projects,
		Platform:     s.cfg.Platform,
	})
	if err != nil {
		s.publishStatus(ctx, s.cfg.Provider, owner, repo, payload.After, git.StatusFailure,
			"manifest: "+err.Error(), git.ProductionContext, "")
		return err
	}

	// The reconciliation is the same as the one of the API path: the
	// manifest decides which apps exist, which webhooks and which builds.
	for _, project := range result.Projects {
		if err := s.reconcileApps(ctx, project.Project, project.Apps); err != nil {
			s.publishStatus(ctx, s.cfg.Provider, owner, repo, payload.After, git.StatusFailure,
				"reconcile: "+err.Error(), git.ProductionContext, "")
			return err
		}
	}
	for _, project := range result.Removed {
		if err := s.reconcileApps(ctx, project, nil); err != nil {
			s.publishStatus(ctx, s.cfg.Provider, owner, repo, payload.After, git.StatusFailure,
				"reconcile: "+err.Error(), git.ProductionContext, "")
			return err
		}
	}

	description := fmt.Sprintf("%d project(s) compiled", len(result.Projects))
	if len(result.Removed) > 0 {
		description = fmt.Sprintf("%s, %d removed", description, len(result.Removed))
	}
	s.publishStatus(ctx, s.cfg.Provider, owner, repo, payload.After, git.StatusSuccess, description, git.ProductionContext, "")
	return nil
}

// gitOpsProjects lists the projects touched by a push. An empty list means
// every project of the repository: it is the safe answer when the payload
// does not tell which files changed.
func (s *Server) gitOpsProjects(payload pushPayload) []string {
	seen := map[string]bool{}
	var out []string
	for _, commit := range payload.Commits {
		files := make([]string, 0, len(commit.Added)+len(commit.Modified)+len(commit.Removed))
		files = append(files, commit.Added...)
		files = append(files, commit.Modified...)
		files = append(files, commit.Removed...)
		for _, file := range files {
			project, ok := promote.IsProjectPath(file, s.cfg.Platform)
			if !ok || project == "" || seen[project] {
				continue
			}
			seen[project] = true
			out = append(out, project)
		}
	}
	return out
}

// storeApps is a small helper kept next to the reconciliation: it returns the
// apps of a project, for the API.
func (s *Server) storeApps(ctx context.Context, project string) ([]store.App, error) {
	return s.cfg.Store.AppsByProject(ctx, project)
}

// waitForGitOpsHook ensures the webhook of the GitOps repository, retrying a
// few times: the control plane can start before Forgejo is ready.
func (s *Server) waitForGitOpsHook(ctx context.Context) {
	for attempt := 1; attempt <= 5; attempt++ {
		if err := s.ensureGitOpsHook(ctx); err == nil {
			break
		} else {
			log.Printf("kuberpack gitops webhook: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// resolveRegistry finds where the images are published.
//
// The registry belongs to the platform, never to the application: the code of
// a third party application comes from another forge, its image is still
// published in the registry of the platform.
func (s *Server) resolveRegistry(ctx context.Context) {
	if s.cfg.Provider == nil {
		return
	}
	if strings.TrimSpace(s.cfg.RegistryURL) == "" {
		s.cfg.RegistryURL = s.cfg.Provider.WebURL()
	}
	if strings.TrimSpace(s.cfg.RegistryUser) != "" {
		return
	}
	user, err := s.cfg.Provider.CurrentUser(ctx)
	if err != nil {
		log.Printf("kuberpack registry user: %v", err)
		return
	}
	s.cfg.RegistryUser = user
	log.Printf("kuberpack registry: %s, namespace %s", s.cfg.RegistryURL, user)
}

// ensureChart writes the chart of the control plane in the GitOps repository.
func (s *Server) ensureChart(ctx context.Context) error {
	if s.cfg.GitOpsURL == "" || strings.TrimSpace(s.cfg.ChartPath) == "" {
		return nil
	}
	result, err := promote.EnsureChart(ctx, promote.ChartRequest{
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		HTTPToken:    s.cfg.Token,
		ChartPath:    s.cfg.ChartPath,
		ChartRef:     s.cfg.Platform.ChartRef,
		Platform:     s.cfg.Platform,
	})
	if err != nil {
		return err
	}
	if result.Changed {
		log.Printf("kuberpack chart: updated in the GitOps repository (%s)", result.CommitSHA)
	}
	return nil
}

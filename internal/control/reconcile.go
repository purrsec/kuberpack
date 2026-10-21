package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/hmacsig"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

// reconcileApps makes the operational state of a project match its manifest.
//
// The manifest is the source of truth: every service that declares a
// repository gets an app record, an HMAC secret and a Forgejo webhook, so the
// push of that repository triggers a build. A service that left the manifest
// loses its app, its webhook and its secret: nothing is left dangling.
//
// The reconciliation is idempotent: reconciling twice does not duplicate a
// webhook, rotate a secret or start a second build.
func (s *Server) reconcileApps(ctx context.Context, project string, apps []promote.ProjectApp) error {
	existing, err := s.cfg.Store.AppsByProject(ctx, project)
	if err != nil {
		return err
	}
	previous := make(map[string]store.App, len(existing))
	for _, app := range existing {
		previous[app.Name] = app
	}

	var errs []error
	declared := make(map[string]bool, len(apps))
	for _, desc := range apps {
		declared[desc.Name] = true
		if err := s.reconcileApp(ctx, project, desc, previous[desc.Name]); err != nil {
			errs = append(errs, err)
		}
	}
	for _, app := range existing {
		if declared[app.Name] {
			continue
		}
		if err := s.removeApp(ctx, app); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reconcileApp aligns one app with its declaration.
func (s *Server) reconcileApp(ctx context.Context, project string, desc promote.ProjectApp, previous store.App) error {
	record := appFromManifest(project, desc)
	app, created, err := s.cfg.Store.UpsertApp(ctx, record)
	if err != nil {
		return fmt.Errorf("app %s: %w", desc.Name, err)
	}

	// The secret is the key of the webhook signature: it is created once and
	// kept, so a reconciliation does not invalidate the pending deliveries.
	secret, err := s.cfg.Secrets.Get(app.ID)
	if err != nil || strings.TrimSpace(secret) == "" {
		secret, err = hmacsig.NewSecret()
		if err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
		if err := s.cfg.Secrets.Put(app.ID, secret); err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
	}

	if s.cfg.WebhookURL != "" {
		owner, repo, err := git.SplitOwnerName(app.ForgejoRepository)
		if err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
		hook, err := s.cfg.Provider.EnsureHook(ctx, owner, repo, s.cfg.WebhookURL, secret)
		if err != nil {
			return fmt.Errorf("app %s: webhook: %w", desc.Name, err)
		}
		if err := s.cfg.Store.SetWebhookID(ctx, app.ID, hook.ID); err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
		app.WebhookID = hook.ID
	}

	// A new app is built right away. An app whose build parameters changed
	// is built again: the manifest asked for something else.
	if created || buildParametersChanged(previous, app) {
		if err := s.queueFirstBuild(ctx, app); err != nil {
			return fmt.Errorf("app %s: %w", desc.Name, err)
		}
	}
	return nil
}

// removeApp drops everything an app owns: the webhook, the secret and the
// record.
func (s *Server) removeApp(ctx context.Context, app store.App) error {
	if app.WebhookID > 0 {
		if owner, repo, err := git.SplitOwnerName(app.ForgejoRepository); err == nil {
			_ = s.cfg.Provider.DeleteHook(ctx, owner, repo, app.WebhookID)
		}
	}
	_ = s.cfg.Secrets.Delete(app.ID)
	return s.cfg.Store.DeleteApp(ctx, app.ID)
}

// appValuesPath is where the digest of an app is written, relative to the
// root of the GitOps repository. An app of a project has its own path; an app
// outside a project falls back to the platform default.
func appValuesPath(platform promote.Platform, app store.App) string {
	if path := strings.TrimSpace(app.ValuesPath); path != "" {
		return path
	}
	return platform.ValuesPath(app.Name)
}

// appFromManifest projects a declared service into the app record. The record
// is a cache of the manifest: the manifest stays the source of truth.
func appFromManifest(project string, desc promote.ProjectApp) store.App {
	internet := true
	if desc.Internet != nil {
		internet = *desc.Internet
	}
	return store.App{
		Name:              desc.Name,
		Project:           project,
		ValuesPath:        desc.ValuesPath,
		ForgejoRepository: desc.Repository,
		ProductionBranch:  desc.Branch,
		Strategy:          desc.Strategy,
		Autodeploy:        true,
		Hostname:          desc.Hostname,
		Port:              desc.Port,
		Healthcheck:       desc.Healthcheck,
		StartCommand:      desc.StartCommand,
		RootDirectory:     desc.RootDirectory,
		BuildCommand:      desc.BuildCommand,
		Internet:          internet,
		Peers:             desc.Peers,
		Secrets:           desc.Secrets,
		SecretEnv:         desc.SecretEnv,
	}
}

// buildParametersChanged reports whether the manifest asked for a different
// build than the one recorded: a new repository, a new branch, a new strategy
// or new build overrides.
func buildParametersChanged(previous, current store.App) bool {
	if previous.ID == 0 {
		return false
	}
	return previous.ForgejoRepository != current.ForgejoRepository ||
		previous.ProductionBranch != current.ProductionBranch ||
		previous.Strategy != current.Strategy ||
		previous.StartCommand != current.StartCommand ||
		previous.RootDirectory != current.RootDirectory ||
		previous.BuildCommand != current.BuildCommand
}

// queueFirstBuild starts the first build of an app. A repository that cannot
// be reached is not an error: the push of that repository will trigger the
// build later.
func (s *Server) queueFirstBuild(ctx context.Context, app store.App) error {
	owner, repo, err := git.SplitOwnerName(app.ForgejoRepository)
	if err != nil {
		return err
	}
	sha, err := s.cfg.Provider.BranchSHA(ctx, owner, repo, app.ProductionBranch)
	if err != nil || strings.TrimSpace(sha) == "" {
		return nil
	}
	del := "register-" + app.Name + "-" + sha + "-" + deliveryNonce()
	if err := s.cfg.Store.InsertDelivery(ctx, store.Delivery{
		ID:        del,
		AppID:     app.ID,
		EventType: "register",
		CommitSHA: sha,
		Status:    "queued",
	}); err != nil && !errors.Is(err, store.ErrDuplicate) {
		return err
	}
	s.publishCommitStatus(ctx, owner, repo, sha, git.StatusPending, "Building", app, false)
	s.kick()
	return nil
}

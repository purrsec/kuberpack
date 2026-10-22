package control

import (
	"context"
	"log"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func promotePreviewHost(app string, pr int, domain string) string {
	return promote.PreviewHostname(app, pr, domain)
}

func archiveURL(gitURL, app, sha string) string {
	gitURL = strings.TrimSpace(strings.TrimSuffix(gitURL, ".git"))
	if gitURL == "" || app == "" || sha == "" {
		return ""
	}
	app = strings.ReplaceAll(strings.ToLower(app), "/", "-")
	return gitURL + "/src/branch/main/" + app + "/" + strings.ToLower(sha)
}

func previewBranch(p promote.Platform) string {
	b := p.ForPreview().PreviewsBranch
	if b == "" {
		return "previews"
	}
	return b
}

func (s *Server) processPreview(ctx context.Context, d store.Delivery) {
	app, err := s.cfg.Store.AppByID(ctx, d.AppID)
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	provider, owner, name, err := s.appForge(app)
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	releaseName := promote.PreviewReleaseName(app.Name, d.PullRequest)
	host := promotePreviewHost(app.Name, d.PullRequest, s.cfg.Platform.PreviewDomain)
	if d.Action == "closed" {
		s.prunePreview(ctx, app, d.PullRequest, releaseName, host)
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "succeeded", "")
		if d.CommitSHA != "" {
			s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusSuccess, "Preview removed", git.PreviewContext, "")
		}
		return
	}
	if host == "" {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", "preview domain is not configured")
		return
	}
	if s.cfg.GitOpsURL != "" {
		if err := promote.EnsurePreviewBranch(ctx, s.cfg.GitOpsURL, previewBranch(s.cfg.Platform), s.cfg.Provider.CloneAuth(s.cfg.Token)); err != nil {
			_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
			return
		}
	}
	buildID, err := s.cfg.Store.InsertBuild(ctx, store.Build{
		AppID:       app.ID,
		DeliveryID:  d.ID,
		Environment: "preview",
		CommitSHA:   d.CommitSHA,
		Status:      "running",
	})
	if err != nil {
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", err.Error())
		return
	}
	_ = s.cfg.Store.UpsertPreview(ctx, store.Preview{
		AppID: app.ID, PullRequest: d.PullRequest, HeadSHA: d.CommitSHA,
		BuildID: buildID, Hostname: host, Status: "building",
	})
	s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusPending, "Building preview", git.PreviewContext, "https://"+host)
	previewPlatform := s.cfg.Platform.ForPreview()
	result, runErr := s.cfg.Run(ctx, release.Request{
		Builder:             s.cfg.Builder,
		BuildID:             buildID,
		DeliveryID:          d.ID,
		Provider:            s.cfg.Provider,
		RegistryURL:         s.cfg.RegistryURL,
		Token:               s.cfg.Token,
		Owner:               owner,
		Name:                name,
		Branch:              app.ProductionBranch,
		SHA:                 d.CommitSHA,
		Strategy:            app.Strategy,
		StartCmd:            app.StartCommand,
		RootDirectory:       app.RootDirectory,
		BuildCommand:        app.BuildCommand,
		GitOpsURL:           s.cfg.GitOpsURL,
		GitOpsBranch:        previewPlatform.PreviewsBranch,
		ValuesPath:          previewPlatform.ValuesPath(releaseName),
		ChartPath:           s.cfg.ChartPath,
		Hostname:            host,
		Port:                app.Port,
		Healthcheck:         app.Healthcheck,
		InheritSecretFrom:   app.InheritSecretFrom,
		PreviewDatabase:     app.PreviewDatabase,
		Platform:            previewPlatform,
		Wait:                s.cfg.Wait,
		AllowNonHEAD:        true,
		BuildsGitURL:        s.cfg.BuildsGitURL,
		BuildsGitAuthScheme: git.AuthScheme(s.cfg.Provider.Name()),
		GitOpsAuthArgs:      s.cfg.Provider.CloneAuth(s.cfg.Token),
		Environment:         "preview",
		Log:                 func(format string, args ...any) { log.Printf(format, args...) },
	})
	finished := store.Build{Status: "succeeded", ArchiveURL: archiveURL(s.cfg.BuildsGitURL, name, d.CommitSHA)}
	finished.Logs = s.buildLogs(ctx, d.ID)
	if runErr != nil {
		finished.Status = "failed"
		finished.Error = runErr.Error()
		_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
		_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "failed", runErr.Error())
		_ = s.cfg.Store.UpsertPreview(ctx, store.Preview{AppID: app.ID, PullRequest: d.PullRequest, HeadSHA: d.CommitSHA, BuildID: buildID, Hostname: host, Status: "failed"})
		s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusFailure, runErr.Error(), git.PreviewContext, finished.ArchiveURL)
		return
	}
	finished.ImageRepository = result.Image.Repository
	finished.ImageTag = result.Image.Tag
	finished.ImageDigest = result.Image.Digest
	finished.InfraCommitSHA = result.InfraCommitSHA
	_ = s.cfg.Store.FinishBuild(ctx, buildID, finished)
	_ = s.cfg.Store.SetDeliveryStatus(ctx, d.ID, "succeeded", "")
	_ = s.cfg.Store.UpsertPreview(ctx, store.Preview{AppID: app.ID, PullRequest: d.PullRequest, HeadSHA: d.CommitSHA, BuildID: buildID, Hostname: host, Status: "ready"})
	s.publishStatus(ctx, provider, owner, name, d.CommitSHA, git.StatusSuccess, "Preview ready", git.PreviewContext, "https://"+host)
}

func (s *Server) prunePreview(ctx context.Context, app store.App, pr int, releaseName, host string) {
	if s.cfg.GitOpsURL != "" {
		_, err := promote.RemovePreview(ctx, promote.PreviewRequest{
			GitOpsURL:    s.cfg.GitOpsURL,
			GitOpsBranch: previewBranch(s.cfg.Platform),
			HTTPToken:    s.cfg.Token,
			AuthArgs:     s.cfg.Provider.CloneAuth(s.cfg.Token),
			App: promote.AppSpec{
				Name:              releaseName,
				Hostname:          host,
				Port:              app.Port,
				Healthcheck:       app.Healthcheck,
				Preview:           true,
				InheritSecretFrom: app.InheritSecretFrom,
				PreviewDatabase:   app.PreviewDatabase,
			},
			Platform: s.cfg.Platform,
		})
		if err != nil {
			log.Printf("kuberpack prune preview %s: %v", releaseName, err)
		}
	}
	_ = s.cfg.Store.UpsertPreview(ctx, store.Preview{
		AppID: app.ID, PullRequest: pr, HeadSHA: "", Hostname: host, Status: "closed",
	})
}

func (s *Server) gcPreviews(ctx context.Context) {
	open, err := s.cfg.Store.ListOpenPreviews(ctx)
	if err != nil {
		return
	}
	ttl := s.cfg.PreviewTTL
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	now := time.Now().UTC()
	for _, p := range open {
		if p.UpdatedAt.IsZero() || now.Sub(p.UpdatedAt) < ttl {
			continue
		}
		app, err := s.cfg.Store.AppByID(ctx, p.AppID)
		if err != nil {
			continue
		}
		releaseName := promote.PreviewReleaseName(app.Name, p.PullRequest)
		s.prunePreview(ctx, app, p.PullRequest, releaseName, p.Hostname)
		_ = s.cfg.Store.UpsertPreview(ctx, store.Preview{AppID: p.AppID, PullRequest: p.PullRequest, HeadSHA: p.HeadSHA, Hostname: p.Hostname, Status: "pruned"})
		log.Printf("kuberpack gc preview %s pr-%d", app.Name, p.PullRequest)
	}
}

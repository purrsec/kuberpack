package control

import (
	"context"
	"errors"
	"log"

	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/release"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

func (s *Server) trackFor(ctx context.Context, app store.App) promote.Track {
	raw, err := s.gitOpsValues(ctx, app)
	if err != nil {
		return promote.Track{Follow: true}
	}
	track, err := promote.ParseTrack(raw)
	if err != nil {
		log.Printf("kuberpack track %s: %v", app.Name, err)
		return promote.Track{Follow: true}
	}
	return track
}

func (s *Server) gitOpsValues(ctx context.Context, app store.App) ([]byte, error) {
	if s.cfg.GitOpsFile != nil {
		return s.cfg.GitOpsFile(ctx, app)
	}
	if s.cfg.GitOpsURL == "" || s.cfg.Client == nil {
		return nil, errors.New("gitops is not configured")
	}
	owner, repo, err := forgejo.RepoOwnerNameFromURL(s.cfg.GitOpsURL)
	if err != nil {
		return nil, err
	}
	path := s.cfg.Platform.ValuesPath(app.Name)
	return s.cfg.Client.RawFile(ctx, owner, repo, path, s.cfg.GitOpsBranch)
}

func (s *Server) alignTracks(ctx context.Context) {
	if s.cfg.GitOpsURL == "" && s.cfg.GitOpsFile == nil {
		return
	}
	apps, err := s.cfg.Store.ListApps(ctx)
	if err != nil {
		log.Printf("kuberpack track: %v", err)
		return
	}
	for _, app := range apps {
		s.alignTrack(ctx, app)
	}
}

func (s *Server) alignTrack(ctx context.Context, app store.App) {
	raw, err := s.gitOpsValues(ctx, app)
	if err != nil {
		return
	}
	meta, err := promote.ParseValuesMeta(raw)
	if err != nil {
		log.Printf("kuberpack track %s: %v", app.Name, err)
		return
	}
	if meta.Track.FollowsMain() {
		return
	}
	if meta.Image != "" {
		if ref, err := image.Parse(meta.Image); err == nil && meta.Track.MatchesCommit(ref.CommitSHA) {
			return
		}
	}
	owner, name, err := forgejo.ParseOwnerName(app.ForgejoRepository)
	if err != nil || s.cfg.Client == nil {
		return
	}
	sha := meta.Track.SHA
	if resolved, err := s.cfg.Client.ResolveCommit(ctx, owner, name, sha); err == nil {
		sha = resolved
	}
	digest := ""
	if b, err := s.cfg.Store.SucceededBuildByCommit(ctx, app.ID, sha); err == nil {
		digest = b.ImageDigest
		if b.CommitSHA != "" {
			sha = b.CommitSHA
		}
	}
	result, err := release.PinExisting(ctx, release.Request{
		Client:       s.cfg.Client,
		Token:        s.cfg.Token,
		Owner:        owner,
		Name:         name,
		SHA:          sha,
		KnownDigest:  digest,
		GitOpsURL:    s.cfg.GitOpsURL,
		GitOpsBranch: s.cfg.GitOpsBranch,
		ValuesPath:   s.cfg.Platform.ValuesPath(app.Name),
		ChartPath:    s.cfg.ChartPath,
		Hostname:     app.Hostname,
		Port:         app.Port,
		Healthcheck:  app.Healthcheck,
		Platform:     s.cfg.Platform,
	})
	if err != nil {
		log.Printf("kuberpack freeze %s at %s: %v", app.Name, sha, err)
		return
	}
	if result.Changed {
		log.Printf("kuberpack froze %s at %s (%s)", app.Name, result.Image, result.InfraCommitSHA)
		s.publishCommitStatus(ctx, owner, name, sha, forgejo.StatusSuccess, "Deployed", app)
	}
}

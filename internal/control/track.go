package control

import (
	"context"
	"errors"
	"log"

	"git.host.bzh/pepe/kuberpack/internal/git"
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
	if s.cfg.GitOpsURL == "" || s.cfg.Provider == nil {
		return nil, errors.New("gitops is not configured")
	}
	owner, repo, err := git.RepoOwnerNameFromURL(s.cfg.GitOpsURL)
	if err != nil {
		return nil, err
	}
	path := appValuesPath(s.cfg.Platform, app)
	raw, err := s.cfg.Provider.RawFile(ctx, owner, repo, path, s.cfg.GitOpsBranch)
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
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
	provider, owner, name, err := s.appForge(app)
	if err != nil || provider == nil {
		return
	}
	sha := meta.Track.SHA
	if resolved, err := provider.ResolveCommit(ctx, owner, name, sha); err == nil {
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
		Provider:            s.cfg.Provider,
		RegistryURL:         s.cfg.RegistryURL,
		RegistryUser:        s.cfg.RegistryUser,
		RegistryToken:       s.cfg.RegistryToken,
		ImageName:           app.Name,
		Token:               s.cfg.Token,
		Owner:               owner,
		Name:                name,
		SHA:                 sha,
		KnownDigest:         digest,
		GitOpsURL:           s.cfg.GitOpsURL,
		BuildsGitAuthScheme: git.AuthScheme(s.cfg.Provider.Name()),
		GitOpsAuthArgs:      s.cfg.Provider.CloneAuth(s.cfg.Token),
		GitOpsBranch:        s.cfg.GitOpsBranch,
		ValuesPath:          appValuesPath(s.cfg.Platform, app),
		ChartPath:           s.cfg.ChartPath,
		Hostname:            app.Hostname,
		Port:                app.Port,
		Healthcheck:         app.Healthcheck,
		Platform:            s.cfg.Platform,
		Egress:              s.cfg.Egress,
	})
	if err != nil {
		log.Printf("kuberpack freeze %s at %s: %v", app.Name, sha, err)
		return
	}
	if result.Changed {
		log.Printf("kuberpack froze %s at %s (%s)", app.Name, result.Image, result.InfraCommitSHA)
		s.publishCommitStatus(ctx, provider, owner, name, sha, git.StatusSuccess, "Deployed", app, false)
	}
}

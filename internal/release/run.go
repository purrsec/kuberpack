package release

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
	"git.host.bzh/pepe/kuberpack/internal/fail"
	"git.host.bzh/pepe/kuberpack/internal/fetch"
	"git.host.bzh/pepe/kuberpack/internal/forgejo"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

type Request struct {
	Builder         builder.Runner
	BuildID         int64
	DeliveryID      string
	Client          *forgejo.Client
	Token           string
	Owner           string
	Name            string
	Branch          string
	SHA             string
	Strategy        string
	StartCmd        string
	GitOpsURL       string
	GitOpsBranch    string
	ValuesPath      string
	ChartPath       string
	Hostname        string
	Port            int
	Healthcheck     string
	Platform        promote.Platform
	ImageRepository string
	RegistryUser    string
	Wait            time.Duration
	SkipPromote     bool
	AllowNonHEAD    bool
	KnownDigest     string
	BuildsGitURL    string
	Environment     string
	Log             func(string, ...any)
}

type Result struct {
	SHA            string
	Image          image.Ref
	App            string
	InfraCommitSHA string
	Changed        bool
	SkippedPromote bool
}

func Run(ctx context.Context, req Request) (Result, error) {
	log := req.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	if req.Wait <= 0 {
		req.Wait = 15 * time.Minute
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	if req.ChartPath == "" {
		req.ChartPath = "charts/stateless"
	}
	if req.Client == nil {
		return Result{}, fail.Text(fail.Clone, "forgejo client is nil")
	}

	kind, err := strategy.ParseKind(req.Strategy)
	if err != nil {
		return Result{}, fail.Stage(fail.Strategy, err)
	}

	repo, err := req.Client.Repo(ctx, req.Owner, req.Name)
	if err != nil {
		return Result{}, fail.Stage(fail.Clone, err)
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA))
	if sha == "" {
		sha, err = req.Client.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
		if err != nil {
			return Result{}, fail.Stage(fail.Clone, err)
		}
	}

	parent, err := os.MkdirTemp("", "kuberpack-release-*")
	if err != nil {
		return Result{}, fail.Stage(fail.Job, err)
	}
	defer os.RemoveAll(parent)
	dest := filepath.Join(parent, "src")
	if err := fetch.Checkout(ctx, repo.CloneURL, sha, dest, fetch.TokenHeaderArgs(req.Token)); err != nil {
		return Result{}, fail.Stage(fail.Clone, err)
	}
	plan, err := strategy.Resolve(dest, kind)
	if err != nil {
		return Result{}, fail.Stage(fail.Strategy, err)
	}

	log("repository: %s", repo.FullName)
	log("branch: %s", req.Branch)
	log("sha: %s", sha)
	log("strategy: %s", plan.Kind)
	log("clone: ok")

	if plan.Kind == strategy.UV {
		if err := strategy.CheckLock(dest); err != nil {
			return Result{}, fail.Stage(fail.Strategy, err)
		}
		log("uv.lock present; Railpack consumes the lockfile")
	}

	ociRepo, err := ImageRepository(req.ImageRepository, req.Client.BaseURL, req.Owner, req.Name)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}

	if req.Builder == nil {
		return Result{}, fail.Text(fail.Job, "builder image is not configured")
	}
	log("submitting Kubernetes build Job for %s", sha)
	buildReq := builder.Request{
		CloneURL:        repo.CloneURL,
		CommitSHA:       sha,
		ImageRepository: ociRepo,
		RegistryUser:    req.Owner,
		StartCmd:        req.StartCmd,
		BuildID:         req.BuildID,
		DeliveryID:      req.DeliveryID,
		BuildsGitURL:    req.BuildsGitURL,
		AppName:         req.Name,
		Environment:     req.Environment,
	}
	if err := req.Builder.Build(ctx, buildReq); err != nil {
		return Result{}, fail.Stage(fail.Job, err)
	}
	log("Kubernetes build Job completed")

	_, ociName, err := oci.SplitRepository(ociRepo)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}

	regUser := req.RegistryUser
	if regUser == "" {
		regUser = req.Owner
	}
	reg, err := oci.New(req.Client.BaseURL, regUser, req.Token)
	if err != nil {
		return Result{}, fail.Stage(fail.Push, err)
	}
	log("waiting for %s:%s", ociRepo, oci.TagForCommit(sha))
	digest, err := reg.ManifestDigest(ctx, ociName, oci.TagForCommit(sha))
	if err != nil {
		return Result{}, fail.Stage(fail.Push, err)
	}
	ref, err := image.Pin(ociRepo, sha, digest)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}
	log("digest: %s", ref.Digest)
	log("image: %s", ref.String())

	if !req.AllowNonHEAD {
		head, err := req.Client.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
		if err != nil {
			return Result{}, fail.Stage(fail.Clone, err)
		}
		if head != sha {
			return Result{}, fail.Text(fail.GitOps, "commit is no longer branch HEAD")
		}
	}

	result := Result{SHA: sha, Image: ref}
	if req.SkipPromote {
		result.SkippedPromote = true
		log("skip-promote")
		return result, nil
	}

	values := req.ValuesPath
	if values == "" {
		values = req.Platform.ValuesPath(req.Name)
	}
	promoted, err := promote.Run(ctx, promote.Request{
		GitOpsURL:    req.GitOpsURL,
		GitOpsBranch: req.GitOpsBranch,
		ValuesPath:   values,
		ChartPath:    req.ChartPath,
		HTTPToken:    req.Token,
		Image:        ref,
		Hostname:     req.Hostname,
		Port:         req.Port,
		Healthcheck:  req.Healthcheck,
		Platform:     req.Platform,
	})
	if err != nil {
		return Result{}, fail.Stage(fail.GitOps, err)
	}
	result.App = promoted.App
	result.InfraCommitSHA = promoted.InfraCommitSHA
	result.Changed = promoted.Changed
	if promoted.Changed {
		log("promoted %s to %s (%s)", promoted.App, promoted.Image, promoted.InfraCommitSHA)
	} else {
		log("%s already at %s (%s)", promoted.App, promoted.Image, promoted.InfraCommitSHA)
	}
	return result, nil
}

func ImageRepository(explicit, forgejoURL, owner, name string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	host := RegistryHost(forgejoURL)
	if host == "" {
		return "", fail.Text(fail.Image, "cannot derive registry host")
	}
	return host + "/" + owner + "/" + name, nil
}

func RegistryHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

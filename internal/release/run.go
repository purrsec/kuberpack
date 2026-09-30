package release

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/builder"
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
	KnownDigest     string
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
		return Result{}, fmt.Errorf("forgejo client is nil")
	}

	kind, err := strategy.ParseKind(req.Strategy)
	if err != nil {
		return Result{}, err
	}

	repo, err := req.Client.Repo(ctx, req.Owner, req.Name)
	if err != nil {
		return Result{}, err
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA))
	if sha == "" {
		sha, err = req.Client.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
		if err != nil {
			return Result{}, err
		}
	}

	parent, err := os.MkdirTemp("", "kuberpack-release-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(parent)
	dest := filepath.Join(parent, "src")

	if err := fetch.Checkout(ctx, repo.CloneURL, sha, dest, fetch.TokenHeaderArgs(req.Token)); err != nil {
		return Result{}, err
	}
	plan, err := strategy.Resolve(dest, kind)
	if err != nil {
		return Result{}, err
	}

	log("repository: %s", repo.FullName)
	log("branch: %s", req.Branch)
	log("sha: %s", sha)
	log("strategy: %s", plan.Kind)
	log("clone: ok")

	if plan.Kind == strategy.UV {
		if err := strategy.CheckLock(dest); err != nil {
			return Result{}, err
		}
		log("uv executor not implemented; building with railpack")
		plan.Kind = strategy.Railpack
	}

	ociRepo, err := ImageRepository(req.ImageRepository, req.Client.BaseURL, req.Owner, req.Name)
	if err != nil {
		return Result{}, err
	}

	switch plan.Kind {
	case strategy.Railpack:
		if req.Builder == nil {
			return Result{}, fmt.Errorf("kubernetes builder is not configured (set KUBERPACK_BUILDER_IMAGE)")
		}
		log("submitting Kubernetes build Job for %s", sha)
		if err := req.Builder.Build(ctx, builder.Request{CloneURL: repo.CloneURL, CommitSHA: sha, ImageRepository: ociRepo, RegistryUser: req.Owner, StartCmd: req.StartCmd, BuildID: req.BuildID, DeliveryID: req.DeliveryID}); err != nil {
			return Result{}, err
		}
		log("Kubernetes build Job completed")
	default:
		return Result{}, fmt.Errorf("no builder configured")
	}

	_, ociName, err := oci.SplitRepository(ociRepo)
	if err != nil {
		return Result{}, err
	}

	regUser := req.RegistryUser
	if regUser == "" {
		regUser = req.Owner
	}
	reg, err := oci.New(req.Client.BaseURL, regUser, req.Token)
	if err != nil {
		return Result{}, err
	}
	log("waiting for %s:%s", ociRepo, oci.TagForCommit(sha))
	digest, err := reg.ManifestDigest(ctx, ociName, oci.TagForCommit(sha))
	if err != nil {
		return Result{}, err
	}
	ref, err := image.Pin(ociRepo, sha, digest)
	if err != nil {
		return Result{}, err
	}
	log("digest: %s", ref.Digest)
	log("image: %s", ref.String())

	head, err := req.Client.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
	if err != nil {
		return Result{}, err
	}
	if head != sha {
		return Result{}, fmt.Errorf("sha %s is no longer HEAD of %s (%s); not promoting", sha, req.Branch, head)
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
		return Result{}, err
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
		return "", fmt.Errorf("cannot derive image repository from Forgejo URL %q", forgejoURL)
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

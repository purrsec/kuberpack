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
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"git.host.bzh/pepe/kuberpack/internal/strategy"
)

type Request struct {
	Builder    builder.Runner
	BuildID    int64
	DeliveryID string
	// Provider is the git forge the application lives in.
	Provider git.Provider
	// RegistryURL is the OCI registry the image is pushed to. It defaults
	// to the web URL of the forge, which is where a Forgejo serves its
	// registry.
	RegistryURL string
	// RegistryUser is the namespace the image is published in. It is the
	// account of the platform, not the owner of the application
	// repository: the code may live in the repository of somebody else.
	RegistryUser string
	// ImageName is the name of the image. It is the name of the app, not
	// the name of the repository it is built from.
	ImageName         string
	Token             string
	Owner             string
	Name              string
	Branch            string
	SHA               string
	Strategy          string
	StartCmd          string
	RootDirectory     string
	BuildCommand      string
	GitOpsURL         string
	GitOpsBranch      string
	ValuesPath        string
	ChartPath         string
	Hostname          string
	Port              int
	Healthcheck       string
	InheritSecretFrom string
	PreviewDatabase   bool
	Platform          promote.Platform
	ImageRepository   string
	Wait              time.Duration
	SkipPromote       bool
	AllowNonHEAD      bool
	KnownDigest       string
	BuildsGitURL      string
	Environment       string
	Log               func(string, ...any)
}

// registryUser is the namespace the image is published in.
func (r Request) registryUser() string {
	if user := strings.TrimSpace(r.RegistryUser); user != "" {
		return user
	}
	return r.Owner
}

// imageName is the name of the image: the name of the app, so that two
// projects can declare a service with the same name.
func (r Request) imageName() string {
	if name := strings.TrimSpace(r.ImageName); name != "" {
		return name
	}
	return r.Name
}

// registryURL is the registry the image is pushed to.
func (r Request) registryURL() string {
	if url := strings.TrimSpace(r.RegistryURL); url != "" {
		return url
	}
	if r.Provider != nil {
		return r.Provider.WebURL()
	}
	return ""
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
	if req.Provider == nil {
		return Result{}, fail.Text(fail.Clone, "forgejo client is nil")
	}

	kind, err := strategy.ParseKind(req.Strategy)
	if err != nil {
		return Result{}, fail.Stage(fail.Strategy, err)
	}

	repo, err := req.Provider.Repository(ctx, req.Owner, req.Name)
	if err != nil {
		return Result{}, fail.Stage(fail.Clone, err)
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA))
	if sha == "" {
		sha, err = req.Provider.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
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

	ociRepo, err := ImageRepository(req.ImageRepository, req.registryURL(), req.registryUser(), req.imageName())
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}

	if req.Builder == nil {
		return Result{}, fail.Text(fail.Job, "builder image is not configured")
	}
	log("submitting Kubernetes build Job for %s", sha)
	buildReq := builder.Request{
		GenerateLock:    plan.GenerateLock,
		CloneURL:        repo.CloneURL,
		CommitSHA:       sha,
		ImageRepository: ociRepo,
		RegistryUser:    req.registryUser(),
		StartCmd:        req.StartCmd,
		RootDirectory:   req.RootDirectory,
		BuildCommand:    req.BuildCommand,
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
	reg, err := oci.New(req.registryURL(), regUser, req.Token)
	if err != nil {
		return Result{}, fail.Stage(fail.Push, err)
	}
	log("waiting for %s:%s", ociRepo, oci.TagForCommit(sha))
	// The registry can lag behind the Job that just pushed the image: retry
	// instead of failing the build on a transient hiccup. The budget is
	// capped so the total wait stays bounded by --wait.
	digest, err := reg.WaitForCommit(ctx, ociName, sha, min(req.Wait, 2*time.Minute), 0)
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
		head, err := req.Provider.BranchSHA(ctx, req.Owner, req.Name, req.Branch)
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
		GitOpsURL:         req.GitOpsURL,
		GitOpsBranch:      req.GitOpsBranch,
		ValuesPath:        values,
		ChartPath:         req.ChartPath,
		HTTPToken:         req.Token,
		Image:             ref,
		Hostname:          req.Hostname,
		Port:              req.Port,
		Healthcheck:       req.Healthcheck,
		InheritSecretFrom: req.InheritSecretFrom,
		PreviewDatabase:   req.PreviewDatabase,
		Platform:          req.Platform,
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

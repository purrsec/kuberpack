package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"git.host.bzh/pepe/kuberpack/internal/netguard"
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
	RegistryUser  string
	RegistryToken string
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
	// BuildsGitAuthScheme is how the forge of the builds repository
	// authenticates a clone.
	BuildsGitAuthScheme string
	// GitOpsAuthArgs authenticate the clone of the GitOps repository. It
	// is the forge of the platform, which is not necessarily the forge of
	// the application.
	GitOpsAuthArgs []string
	Environment    string
	// Egress garde les accès sortants déclenchés par le manifeste (clone,
	// registre). Nil retombe sur la politique de l'environnement.
	Egress *netguard.Policy
	// EgressAllow est la même allowlist, sous sa forme texte, pour le Job
	// du builder : il clone le dépôt de l'application avec la même politique.
	EgressAllow string
	Log         func(string, ...any)
}

// checkout clones the repository of the application. It is a variable so
// that a test can observe how the clone authenticates itself.
var checkout = fetch.Checkout

// gitOpsAuthArgs is how the clone of the GitOps repository authenticates.
// The GitOps repository lives on the forge of the platform; the application
// may live on another forge.
func (r Request) gitOpsAuthArgs() []string {
	if len(r.GitOpsAuthArgs) > 0 {
		return r.GitOpsAuthArgs
	}
	if r.Provider == nil {
		return nil
	}
	return r.Provider.CloneAuth(r.Token)
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
		req.ChartPath = "provisioning/charts/stateless"
	}
	if req.Provider == nil {
		return Result{}, fail.Text(fail.Clone, "forgejo client is nil")
	}
	if strings.TrimSpace(req.RegistryToken) == "" {
		return Result{}, fail.Text(fail.Auth, "registry token is required; configure KUBERPACK_REGISTRY_TOKEN separately from the GitOps token")
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
	// The forge of the application has its own way to authenticate a clone:
	// the token of a Gitea in a header is an invalid credential for GitHub,
	// which then asks for a username on the terminal.
	cloneAuth, err := applicationCloneAuth(req.Provider, req.Token, repo)
	if err != nil {
		return Result{}, fail.Stage(fail.Clone, err)
	}
	if req.Egress != nil {
		err = fetch.CheckoutWithPolicy(ctx, repo.CloneURL, sha, dest, cloneAuth, req.Egress)
	} else {
		err = checkout(ctx, repo.CloneURL, sha, dest, cloneAuth)
	}
	if err != nil {
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
		GenerateLock: plan.GenerateLock,
		// The builder must not hold the token of the control plane. It
		// is given the fingerprint of it, so that it can tell.
		ControlTokenDigest: tokenDigest(req.Token),
		StrictTokens:       strictTokens(),
		// The forge of the application has its own way to authenticate a
		// clone: the builder must use it, not the way of the platform.
		GitAuthScheme:       git.AuthScheme(req.Provider.Name()),
		CloneAnonymous:      !repo.Private,
		CloneURL:            repo.CloneURL,
		CommitSHA:           sha,
		ImageRepository:     ociRepo,
		RegistryUser:        req.registryUser(),
		StartCmd:            req.StartCmd,
		RootDirectory:       req.RootDirectory,
		BuildCommand:        req.BuildCommand,
		BuildID:             req.BuildID,
		DeliveryID:          req.DeliveryID,
		BuildsGitURL:        req.BuildsGitURL,
		BuildsGitAuthScheme: req.BuildsGitAuthScheme,
		EgressAllow:         req.EgressAllow,
		AppName:             req.Name,
		Environment:         req.Environment,
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
	reg, err := oci.New(req.registryURL(), regUser, req.RegistryToken)
	if err != nil {
		return Result{}, fail.Stage(fail.Push, err)
	}
	reg.Policy = req.Egress
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
		AuthArgs:          req.gitOpsAuthArgs(),
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	// A registry is addressed by its host, without a scheme: "ghcr.io".
	host := raw
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	return host
}

// tokenDigest is the fingerprint of a token. It cannot be reversed: it
// tells two tokens apart without revealing either of them.
func tokenDigest(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// strictTokens tells whether the platform refuses a build whose builder
// holds the token of the control plane. Separation is enforced by default;
// a legacy operator can explicitly request warning-only behavior.
func strictTokens() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("KUBERPACK_STRICT_CREDENTIALS")), "false")
}

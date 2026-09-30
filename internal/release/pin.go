package release

import (
	"context"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/fail"
	"git.host.bzh/pepe/kuberpack/internal/image"
	"git.host.bzh/pepe/kuberpack/internal/oci"
	"git.host.bzh/pepe/kuberpack/internal/promote"
)

// PinExisting writes GitOps image from an already published sha-<commit>.
// It does not build and does not require the SHA to be branch HEAD.
func PinExisting(ctx context.Context, req Request) (Result, error) {
	if req.Client == nil {
		return Result{}, fail.Text(fail.Clone, "forgejo client is nil")
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA))
	if sha == "" {
		return Result{}, fail.Text(fail.Clone, "commit SHA is empty")
	}
	if req.GitOpsBranch == "" {
		req.GitOpsBranch = "main"
	}
	if req.ChartPath == "" {
		req.ChartPath = "charts/stateless"
	}

	ociRepo, err := ImageRepository(req.ImageRepository, req.Client.BaseURL, req.Owner, req.Name)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}
	_, ociName, err := oci.SplitRepository(ociRepo)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
	}
	regUser := req.RegistryUser
	if regUser == "" {
		regUser = req.Owner
	}
	digest := strings.TrimSpace(req.digestOverride())
	if digest == "" {
		reg, err := oci.New(req.Client.BaseURL, regUser, req.Token)
		if err != nil {
			return Result{}, fail.Stage(fail.Push, err)
		}
		digest, err = reg.DigestForCommit(ctx, ociName, sha)
		if err != nil {
			return Result{}, fail.Stage(fail.Push, err)
		}
	}
	ref, err := image.Pin(ociRepo, sha, digest)
	if err != nil {
		return Result{}, fail.Stage(fail.Image, err)
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
	return Result{
		SHA:            sha,
		Image:          ref,
		App:            promoted.App,
		InfraCommitSHA: promoted.InfraCommitSHA,
		Changed:        promoted.Changed,
	}, nil
}

func (req Request) digestOverride() string {
	return strings.TrimSpace(req.KnownDigest)
}

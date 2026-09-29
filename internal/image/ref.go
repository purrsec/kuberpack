package image

import (
	"fmt"
	"regexp"
	"strings"
)

const digestPrefix = "sha256:"

var (
	commitSHARe = regexp.MustCompile(`^[a-f0-9]{7,40}$`)
	digestHexRe = regexp.MustCompile(`^[a-f0-9]{64}$`)
	tagRe       = regexp.MustCompile(`^sha-([a-f0-9]{7,40})$`)
)

// Ref is a digest-pinned OCI reference used in GitOps.
type Ref struct {
	Repository string
	Tag        string
	Digest     string
	CommitSHA  string
}

// Pin builds registry/app:sha-<commit>@sha256:<digest>.
func Pin(repository, commitSHA, digest string) (Ref, error) {
	repository = strings.TrimSpace(repository)
	commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
	digest = strings.TrimSpace(digest)

	if err := validateRepository(repository); err != nil {
		return Ref{}, err
	}
	if !commitSHARe.MatchString(commitSHA) {
		return Ref{}, fmt.Errorf("commit SHA must be 7-40 lowercase hex characters")
	}
	normalized, err := normalizeDigest(digest)
	if err != nil {
		return Ref{}, err
	}

	ref := Ref{
		Repository: repository,
		Tag:        "sha-" + commitSHA,
		Digest:     normalized,
		CommitSHA:  commitSHA,
	}
	if err := rejectMutableTag(ref.Tag); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// Parse accepts only digest-pinned sha-<commit> references.
func Parse(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("image reference is empty")
	}
	repoTag, digest, ok := strings.Cut(s, "@")
	if !ok || digest == "" {
		return Ref{}, fmt.Errorf("image reference must include a digest")
	}
	normalized, err := normalizeDigest(digest)
	if err != nil {
		return Ref{}, err
	}

	repository, tag, err := splitRepositoryTag(repoTag)
	if err != nil {
		return Ref{}, err
	}
	if err := rejectMutableTag(tag); err != nil {
		return Ref{}, err
	}
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return Ref{}, fmt.Errorf("image tag %q must be sha-<commit>", tag)
	}

	return Ref{
		Repository: repository,
		Tag:        tag,
		Digest:     normalized,
		CommitSHA:  m[1],
	}, nil
}

func (r Ref) String() string {
	return r.Repository + ":" + r.Tag + "@" + r.Digest
}

func validateRepository(repo string) error {
	if repo == "" {
		return fmt.Errorf("image repository is empty")
	}
	if strings.ContainsAny(repo, " \t") {
		return fmt.Errorf("image repository must not contain whitespace")
	}
	if strings.Contains(repo, "://") {
		return fmt.Errorf("image repository must not include a URL scheme")
	}
	if strings.Contains(repo, "@") {
		return fmt.Errorf("image repository must not include a digest")
	}
	if strings.HasSuffix(repo, "/") {
		return fmt.Errorf("image repository must not end with '/'")
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("image repository must not include a tag")
	}
	return nil
}

func splitRepositoryTag(repoTag string) (string, string, error) {
	slash := strings.LastIndex(repoTag, "/")
	colon := strings.LastIndex(repoTag, ":")
	if colon < 0 || colon < slash {
		return "", "", fmt.Errorf("image reference must include a tag")
	}
	repository := repoTag[:colon]
	tag := repoTag[colon+1:]
	if err := validateRepository(repository); err != nil {
		return "", "", err
	}
	if tag == "" {
		return "", "", fmt.Errorf("image tag is empty")
	}
	return repository, tag, nil
}

func normalizeDigest(digest string) (string, error) {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if digest == "" {
		return "", fmt.Errorf("image digest is empty")
	}
	hex := strings.TrimPrefix(digest, digestPrefix)
	if hex == digest {
		return "", fmt.Errorf("image digest must start with %s", digestPrefix)
	}
	if !digestHexRe.MatchString(hex) {
		return "", fmt.Errorf("image digest must be sha256 followed by 64 hex characters")
	}
	return digestPrefix + hex, nil
}

func rejectMutableTag(tag string) error {
	lower := strings.ToLower(tag)
	if lower == "latest" || lower == "main" || strings.HasPrefix(lower, "main-") {
		return fmt.Errorf("mutable image tag %q is not allowed", tag)
	}
	return nil
}

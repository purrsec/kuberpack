package oci

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWait     = 15 * time.Minute
	defaultInterval = 8 * time.Second
	cobayeShortSHA  = 12
)

// TagForCommit is the product OCI tag. GitOps always pins this tag plus a digest.
func TagForCommit(commitSHA string) string {
	return "sha-" + strings.ToLower(strings.TrimSpace(commitSHA))
}

// DigestForCommit returns Docker-Content-Digest for a built commit.
//
// It prefers the product tag sha-<commit>. Until the cobaye homelab
// railpack-release.sh is replaced, it also accepts a digest published under
// that draft's main-<run>-<12hex> tag. Kubernetes pulls by digest; GitOps
// still writes sha-<commit>@digest.
func (r *Registry) DigestForCommit(ctx context.Context, repository, commitSHA string) (string, error) {
	commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
	if commitSHA == "" {
		return "", fmt.Errorf("commit SHA is empty")
	}

	digest, err := r.ManifestDigest(ctx, repository, TagForCommit(commitSHA))
	if err == nil {
		return digest, nil
	}
	if !IsNotFound(err) {
		return "", err
	}

	tags, listErr := r.Tags(ctx, repository)
	if listErr != nil {
		if IsNotFound(listErr) {
			return "", err
		}
		return "", listErr
	}
	tag, ok := cobayeTagForCommit(tags, commitSHA)
	if !ok {
		return "", err
	}
	return r.ManifestDigest(ctx, repository, tag)
}

// WaitForCommit polls until DigestForCommit succeeds or the deadline expires.
func (r *Registry) WaitForCommit(ctx context.Context, repository, commitSHA string, timeout, interval time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = defaultWait
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		digest, err := r.DigestForCommit(ctx, repository, commitSHA)
		if err == nil {
			return digest, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		if !IsNotFound(err) {
			return "", err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return "", fmt.Errorf("timed out waiting for %s:%s: %w", repository, TagForCommit(commitSHA), lastErr)
}

func cobayeTagForCommit(tags []string, commitSHA string) (string, bool) {
	short := commitSHA
	if len(short) > cobayeShortSHA {
		short = short[:cobayeShortSHA]
	}
	suffix := "-" + short
	best := ""
	bestSeq := -1
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "main-") || !strings.HasSuffix(tag, suffix) {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(tag, "main-"), suffix)
		seq, err := strconv.Atoi(mid)
		if err != nil {
			if best == "" {
				best = tag
			}
			continue
		}
		if seq >= bestSeq {
			bestSeq = seq
			best = tag
		}
	}
	return best, best != ""
}

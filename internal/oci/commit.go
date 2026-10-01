package oci

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	defaultWait     = 15 * time.Minute
	defaultInterval = 8 * time.Second
)

// TagForCommit is the product OCI tag. GitOps always pins this tag plus a digest.
func TagForCommit(commitSHA string) string {
	return "sha-" + strings.ToLower(strings.TrimSpace(commitSHA))
}

// DigestForCommit returns Docker-Content-Digest for repository:sha-<commit>.
func (r *Registry) DigestForCommit(ctx context.Context, repository, commitSHA string) (string, error) {
	commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
	if commitSHA == "" {
		return "", fmt.Errorf("commit SHA is empty")
	}
	return r.ManifestDigest(ctx, repository, TagForCommit(commitSHA))
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

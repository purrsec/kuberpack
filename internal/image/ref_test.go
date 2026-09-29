package image

import (
	"strings"
	"testing"
)

const (
	validSHA    = "0123456789abcdef0123456789abcdef01234567"
	validDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestPin(t *testing.T) {
	ref, err := Pin("git.host.bzh/pepe/web", validSHA, validDigest)
	if err != nil {
		t.Fatal(err)
	}
	want := "git.host.bzh/pepe/web:sha-" + validSHA + "@" + validDigest
	if ref.String() != want {
		t.Fatalf("got %q want %q", ref.String(), want)
	}
	if ref.CommitSHA != validSHA {
		t.Fatalf("commit SHA: got %q", ref.CommitSHA)
	}
}

func TestPinNormalizes(t *testing.T) {
	ref, err := Pin(" git.host.bzh:5000/pepe/web ", strings.ToUpper(validSHA), "SHA256:"+strings.ToUpper(strings.TrimPrefix(validDigest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	if ref.CommitSHA != validSHA {
		t.Fatalf("expected lowercase SHA, got %q", ref.CommitSHA)
	}
	if ref.Digest != validDigest {
		t.Fatalf("expected lowercase digest, got %q", ref.Digest)
	}
	if ref.Repository != "git.host.bzh:5000/pepe/web" {
		t.Fatalf("repository: got %q", ref.Repository)
	}
}

func TestPinRejects(t *testing.T) {
	tests := []struct {
		name, repo, sha, digest string
	}{
		{"empty repo", "", validSHA, validDigest},
		{"tag in repo", "git.host.bzh/pepe/web:latest", validSHA, validDigest},
		{"digest in repo", "git.host.bzh/pepe/web@" + validDigest, validSHA, validDigest},
		{"scheme", "https://git.host.bzh/pepe/web", validSHA, validDigest},
		{"short sha", "git.host.bzh/pepe/web", "abc", validDigest},
		{"non-hex sha", "git.host.bzh/pepe/web", "zzzzzzz", validDigest},
		{"missing digest prefix", "git.host.bzh/pepe/web", validSHA, strings.TrimPrefix(validDigest, "sha256:")},
		{"short digest", "git.host.bzh/pepe/web", validSHA, "sha256:abc"},
		{"empty digest", "git.host.bzh/pepe/web", validSHA, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Pin(tt.repo, tt.sha, tt.digest); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParse(t *testing.T) {
	raw := "git.host.bzh/pepe/web:sha-" + validSHA + "@" + validDigest
	ref, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ref.String() != raw {
		t.Fatalf("got %q want %q", ref.String(), raw)
	}
}

func TestParseRejectsMutableAndUnpinned(t *testing.T) {
	tests := []string{
		"git.host.bzh/pepe/web:latest@" + validDigest,
		"git.host.bzh/pepe/web:main@" + validDigest,
		"git.host.bzh/pepe/web:main-1-" + validSHA[:7] + "@" + validDigest,
		"git.host.bzh/pepe/web:sha-" + validSHA,
		"git.host.bzh/pepe/web@" + validDigest,
		"git.host.bzh/pepe/web:stable@" + validDigest,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	ref, err := Pin("localhost:5000/app", validSHA, validDigest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(ref.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != ref {
		t.Fatalf("got %#v want %#v", got, ref)
	}
}

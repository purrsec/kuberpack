package oci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestManifestDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/pepe/web/manifests/sha-abc" {
			t.Errorf("path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("missing Authorization")
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	reg, err := New(srv.URL, "pepe", "token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.ManifestDigest(context.Background(), "pepe/web", "sha-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != testDigest {
		t.Fatalf("got %q", got)
	}
}

func TestDigestForCommitPrefersProductTag(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/pepe/web/manifests/"+TagForCommit(sha), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/pepe/web/tags/list", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not list tags when sha-<commit> exists")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	reg, err := New(srv.URL, "pepe", "token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.DigestForCommit(context.Background(), "pepe/web", sha)
	if err != nil {
		t.Fatal(err)
	}
	if got != testDigest {
		t.Fatalf("got %q", got)
	}
}

func TestDigestForCommitRequiresProductTag(t *testing.T) {
	sha := "d45ace010726ffffffffffffffffffffffffffff"
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/pepe/hello-world/manifests/"+TagForCommit(sha), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/v2/pepe/hello-world/tags/list", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not fall back to other tags")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	reg, err := New(srv.URL, "pepe", "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.DigestForCommit(context.Background(), "pepe/hello-world", sha); !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitForCommit(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/pepe/web/manifests/"+TagForCommit(sha), func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	reg, err := New(srv.URL, "pepe", "token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.WaitForCommit(context.Background(), "pepe/web", sha, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != testDigest {
		t.Fatalf("got %q", got)
	}
	if hits < 3 {
		t.Fatalf("hits: %d", hits)
	}
}

func TestSplitRepository(t *testing.T) {
	host, name, err := SplitRepository("git.host.bzh/pepe/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if host != "git.host.bzh" || name != "pepe/hello-world" {
		t.Fatalf("got %q %q", host, name)
	}
}

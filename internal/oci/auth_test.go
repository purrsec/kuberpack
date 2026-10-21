package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A hosted registry (GHCR, ECR, GAR) refuses the credentials and asks for a
// token. The client trades the credentials against the token service and
// retries.
func TestRegistryExchangesTheCredentialsForAToken(t *testing.T) {
	var sawBasic, sawBearer bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			// The token service wants the credentials of the platform.
			user, password, ok := r.BasicAuth()
			if !ok || user != "pepe" || password != "pat" {
				t.Errorf("the token service must receive the credentials: %q %q", user, password)
			}
			if r.URL.Query().Get("scope") != "repository:pepe/app:pull" {
				t.Errorf("the scope must be forwarded: %q", r.URL.Query().Get("scope"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "a-token"})
		case "/v2/pepe/app/manifests/latest":
			if r.Header.Get("Authorization") == "Bearer a-token" {
				sawBearer = true
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Docker-Content-Digest", "sha256:1111111111111111111111111111111111111111111111111111111111111111")
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
				return
			}
			sawBasic = true
			w.Header().Set("Www-Authenticate", `Bearer realm="`+server.URL+`/token",service="registry.test",scope="repository:pepe/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	registry, err := New(server.URL, "pepe", "pat")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := registry.ManifestDigest(context.Background(), "pepe/app", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if manifest == "" {
		t.Fatal("the manifest must be read after the exchange")
	}
	if !sawBasic || !sawBearer {
		t.Fatalf("the registry must be asked twice: basic=%v bearer=%v", sawBasic, sawBearer)
	}
}

// The challenge is read whatever the order of its parameters.
func TestParseChallenge(t *testing.T) {
	challenge, err := parseChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:user/image:pull"`)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.realm != "https://ghcr.io/token" || challenge.service != "ghcr.io" {
		t.Fatalf("challenge: %+v", challenge)
	}
	if challenge.scope != "repository:user/image:pull" {
		t.Fatalf("scope: %q", challenge.scope)
	}
	if _, err := parseChallenge(`Basic realm="x"`); err == nil {
		t.Fatal("a basic challenge must be refused")
	}
	if _, err := parseChallenge(""); err == nil {
		t.Fatal("an empty challenge must be refused")
	}
}

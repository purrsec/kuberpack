package oci

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/netguard"
)

func loopbackPolicy(t *testing.T) *netguard.Policy {
	t.Helper()
	policy, err := netguard.Parse([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// L'hôte d'une image vient d'un manifeste : sans allowlist, un registre sur la
// boucle locale est refusé avant toute connexion.
func TestResolveDigestRefusesLoopbackWithoutAllowlist(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "")
	_, err := ResolveDigest(context.Background(), "127.0.0.1:5000/library/app:1.0")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("le registre de la boucle locale devait être refusé: %v", err)
	}
}

func TestResolveDigestRefusesMetadataName(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "")
	_, err := ResolveDigest(context.Background(), "metadata.google.internal/library/app:1.0")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("le nom des métadonnées devait être refusé: %v", err)
	}
}

func TestResolveDigestRefusesPrivateAddress(t *testing.T) {
	t.Setenv(netguard.EnvAllow, "")
	_, err := ResolveDigest(context.Background(), "10.20.30.40/library/app:1.0")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("l'adresse privée devait être refusée: %v", err)
	}
}

// Le service de jeton annoncé par un registre est une nouvelle destination :
// il est gardé comme le registre lui-même.
func TestRegistryExchangeRefusesMetadataRealm(t *testing.T) {
	registry, err := New("https://registry.example", "images", "test-only-token")
	if err != nil {
		t.Fatal(err)
	}
	registry.Policy = netguard.New()
	_, err = registry.exchange(context.Background(), bearerChallenge{realm: "http://169.254.169.254/token"})
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("le service de jeton des métadonnées devait être refusé: %v", err)
	}
	if strings.Contains(err.Error(), "test-only-token") {
		t.Fatal("l'erreur ne doit pas exposer le credential")
	}
}

func TestRegistryRefusesRedirectToMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	registry, err := New(server.URL, "images", "test-only-token")
	if err != nil {
		t.Fatal(err)
	}
	registry.Policy = loopbackPolicy(t)
	_, err = registry.ManifestDigest(context.Background(), "pepe/web", "latest")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("la redirection vers les métadonnées devait être refusée: %v", err)
	}
}

func TestRegistryBoundsRedirects(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()

	registry, err := New(server.URL, "images", "test-only-token")
	if err != nil {
		t.Fatal(err)
	}
	registry.Policy = loopbackPolicy(t)
	_, err = registry.ManifestDigest(context.Background(), "pepe/web", "latest")
	if err == nil {
		t.Fatal("une boucle de redirections devait s'arrêter")
	}
	if hits > netguard.MaxRedirects+1 {
		t.Fatalf("trop de requêtes suivies: %d", hits)
	}
}

// Un client injecté reste prioritaire : les tests existants peuvent parler à
// un serveur local sans allowlist, mais l'appelant garde la responsabilité.
func TestInjectedHTTPClientIsKept(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	registry, err := New(server.URL, "images", "test-only-token")
	if err != nil {
		t.Fatal(err)
	}
	registry.HTTPClient = server.Client()
	if _, err := registry.ManifestDigest(context.Background(), "pepe/web", "latest"); err != nil {
		t.Fatal(err)
	}
}

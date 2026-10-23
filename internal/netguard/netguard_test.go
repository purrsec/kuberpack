package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Les adresses qu'une origine publique ne doit jamais joindre.
func TestPolicyRefusesInternalAddresses(t *testing.T) {
	policy := New()
	for _, host := range []string{
		"127.0.0.1",
		"127.1.2.3",
		"::1",
		"169.254.169.254", // service de métadonnées des fournisseurs
		"fe80::1",
		"10.0.0.1",
		"172.16.4.5",
		"192.168.1.10",
		"fc00::1",
		"fd12:3456::1",
		"224.0.0.1",
		"ff02::1",
		"0.0.0.0",
		"::",
		"100.64.0.1",
		"198.18.0.1",
		"64:ff9b::a00:1",
		"2002:c0a8:0101::1",
	} {
		if err := policy.CheckHost(host); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s aurait dû être refusé: %v", host, err)
		}
	}
}

func TestPolicyAcceptsPublicAddresses(t *testing.T) {
	policy := New()
	for _, host := range []string{"93.184.216.34", "2606:2800:220:1::1", "8.8.8.8", "git.host.bzh", "ghcr.io"} {
		if err := policy.CheckHost(host); err != nil {
			t.Errorf("%s aurait dû être accepté: %v", host, err)
		}
	}
}

func TestPolicyRefusesReservedNames(t *testing.T) {
	policy := New()
	for _, host := range []string{
		"localhost",
		"localhost:8080",
		"metadata.google.internal",
		"metadata",
		"instance-data",
		"kubernetes.default.svc",
		"vault.internal",
		"service.local",
		"postgres.svc",
		"example.cluster.local",
		"thing.home.arpa",
	} {
		if err := policy.CheckHost(host); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s aurait dû être refusé: %v", host, err)
		}
	}
}

func TestAllowlistAuthorizesPrivateDestinations(t *testing.T) {
	policy, err := Parse([]string{"127.0.0.1", "10.0.0.0/8", "*.corp.example", ".internal", "registry.local:5000"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{
		"127.0.0.1",
		"127.0.0.1:5000",
		"10.4.5.6",
		"10.4.5.6:443",
		"api.corp.example",
		"vault.internal",
		"registry.local",
		"registry.local:5000",
	} {
		if err := policy.CheckHost(host); err != nil {
			t.Errorf("%s aurait dû être autorisé: %v", host, err)
		}
	}
	// L'entrée « *.corp.example » n'autorise pas le domaine nu.
	if err := policy.CheckHost("corp.example"); err != nil {
		t.Errorf("le domaine nu doit rester public, pas privé: %v", err)
	}
	if err := policy.CheckHost("other.example"); err != nil {
		t.Errorf("un nom public reste autorisé: %v", err)
	}
	if err := policy.CheckHost("192.168.1.1"); !errors.Is(err, ErrBlocked) {
		t.Error("une plage privée non listée doit rester refusée")
	}
	// « .internal » est explicitement autorisé : les métadonnées de ce
	// suffixe appartiennent à l'opérateur, pas à une origine publique.
	if err := policy.CheckHost("metadata.google.internal"); err != nil {
		t.Errorf("un nom couvert par l'allowlist doit être accepté: %v", err)
	}
}

func TestInvalidAllowlistEntryIsAnError(t *testing.T) {
	if _, err := Parse([]string{"10.0.0.0/nope"}); err == nil {
		t.Fatal("un CIDR invalide doit être une erreur")
	}
}

func TestTrustURLsAcceptsConfiguredHosts(t *testing.T) {
	policy := New().TrustURLs("https://git.corp.example/pepe/repo.git", "registry.corp.example:5000", "", "")
	if err := policy.CheckHost("git.corp.example"); err != nil {
		t.Fatalf("forge configurée refusée: %v", err)
	}
	if err := policy.CheckHost("registry.corp.example"); err != nil {
		t.Fatalf("registre configuré refusé: %v", err)
	}
}

// Le DialContext refuse une adresse interdite avant toute connexion.
func TestDialContextRefusesLoopbackByDefault(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	policy := New()
	client := policy.HTTPClient(2 * time.Second)
	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("une connexion à la boucle locale devait échouer")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("erreur inattendue: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("le serveur local a été joint %d fois", hits.Load())
	}
}

func TestHTTPClientWorksWhenLoopbackIsAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	policy, err := Parse([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := policy.HTTPClient(2 * time.Second).Get(server.URL)
	if err != nil {
		t.Fatalf("la boucle locale autorisée est refusée: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("statut inattendu: %d", response.StatusCode)
	}
}

// Une redirection vers le service de métadonnées ne doit jamais être suivie.
func TestRedirectToMetadataIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	policy, err := Parse([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := policy.HTTPClient(2 * time.Second).Get(server.URL)
	if err == nil {
		response.Body.Close()
		t.Fatal("la redirection vers les métadonnées devait échouer")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("erreur inattendue: %v", err)
	}
}

func TestRedirectsAreBounded(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()

	policy, err := Parse([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = policy.HTTPClient(2 * time.Second).Get(server.URL)
	if err == nil {
		t.Fatal("une boucle de redirections devait s'arrêter")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("erreur inattendue: %v", err)
	}
	if hits.Load() > MaxRedirects+1 {
		t.Fatalf("trop de requêtes suivies: %d", hits.Load())
	}
}

func TestCheckRemoteURLRejectsPrivateAndMetadata(t *testing.T) {
	policy := New()
	for _, raw := range []string{
		"http://10.0.0.1/repo.git",
		"https://169.254.169.254/repo.git",
		"http://metadata.google.internal/computeMetadata/v1/",
		"ssh://git@git.host.bzh/pepe/repo.git",
		"https://user:secret@git.host.bzh/pepe/repo.git",
	} {
		if err := policy.CheckRemoteURL(context.Background(), raw); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s aurait dû être refusé: %v", raw, err)
		}
	}
	if err := policy.CheckRemoteURL(context.Background(), "/tmp/local-repo"); err != nil {
		t.Errorf("un chemin local doit rester accepté: %v", err)
	}
}

func TestAllowedAddrsFiltersAResolvedSet(t *testing.T) {
	policy := New()
	mixed := []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("93.184.216.34"),
	}
	allowed := policy.allowedAddrs(mixed, "tcp")
	if len(allowed) != 1 || allowed[0].String() != "93.184.216.34" {
		t.Fatalf("adresses retenues: %v", allowed)
	}
	// Une entrée explicite réintroduit une adresse privée.
	withAllow, err := Parse([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	allowed = withAllow.allowedAddrs(mixed, "tcp")
	if len(allowed) != 2 {
		t.Fatalf("adresses retenues: %v", allowed)
	}
}

func TestIPv4MappedAddressesAreUnmapped(t *testing.T) {
	policy := New()
	for _, host := range []string{"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1"} {
		if err := policy.CheckHost(host); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s aurait dû être refusé: %v", host, err)
		}
	}
}

func TestHostWithTrailingDotIsNormalized(t *testing.T) {
	policy := New()
	if err := policy.CheckHost("metadata.google.internal."); !errors.Is(err, ErrBlocked) {
		t.Fatalf("le point final ne doit pas contourner le refus: %v", err)
	}
	if err := policy.CheckHost("LOCALHOST"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("la casse ne doit pas contourner le refus: %v", err)
	}
	if !strings.Contains(ErrBlocked.Error(), "interdit") {
		t.Fatal("le message d'erreur doit rester lisible")
	}
}

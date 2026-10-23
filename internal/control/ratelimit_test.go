package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func apiRequest(server *Server, remote, forwardedFor string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/apps", nil)
	request.RemoteAddr = remote
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

// La limite est par client : deux IP différentes ne partagent pas le seau.
func TestAPIRateLimitIsPerClientIP(t *testing.T) {
	server := New(Config{APIToken: "api", APIRateLimit: 2, APIRateBurst: 2, APIMaxConcurrent: -1})
	if got := apiRequest(server, "198.51.100.1:1111", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("première requête: HTTP %d", got)
	}
	if got := apiRequest(server, "198.51.100.1:2222", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("deuxième requête: HTTP %d", got)
	}
	limited := apiRequest(server, "198.51.100.1:3333", "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("la troisième requête devait être limitée: HTTP %d", limited.Code)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("une réponse 429 doit annoncer Retry-After")
	}
	if got := apiRequest(server, "198.51.100.2:1111", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("un autre client devait passer: HTTP %d", got)
	}
}

// La sonde de readiness ne doit jamais recevoir 429.
func TestHealthzIsExemptFromRateLimit(t *testing.T) {
	server := New(Config{APIRateLimit: 1, APIRateBurst: 1, APIMaxConcurrent: -1})
	for index := 0; index < 5; index++ {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = "198.51.100.1:1111"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("sonde %d: HTTP %d", index, response.Code)
		}
	}
}

// Sans proxy de confiance, X-Forwarded-For est une valeur du client : elle ne
// change pas la clé de la limite.
func TestForwardedForIsIgnoredWithoutTrustedProxy(t *testing.T) {
	server := New(Config{APIToken: "api", APIRateLimit: 1, APIRateBurst: 1, APIMaxConcurrent: -1})
	if got := apiRequest(server, "198.51.100.1:1111", "203.0.113.1").Code; got != http.StatusUnauthorized {
		t.Fatalf("première requête: HTTP %d", got)
	}
	if got := apiRequest(server, "198.51.100.1:1111", "203.0.113.2").Code; got != http.StatusTooManyRequests {
		t.Fatalf("un en-tête forgé ne doit pas créer un nouveau seau: HTTP %d", got)
	}
}

// Derrière un proxy déclaré, l'IP réelle du client vient de X-Forwarded-For.
func TestForwardedForIsReadFromATrustedProxy(t *testing.T) {
	server := New(Config{
		APIToken:         "api",
		APIRateLimit:     1,
		APIRateBurst:     1,
		APIMaxConcurrent: -1,
		TrustedProxies:   []string{"10.0.0.0/8"},
	})
	if got := apiRequest(server, "10.1.2.3:9999", "203.0.113.10").Code; got != http.StatusUnauthorized {
		t.Fatalf("client 1: HTTP %d", got)
	}
	if got := apiRequest(server, "10.1.2.3:9999", "203.0.113.10").Code; got != http.StatusTooManyRequests {
		t.Fatalf("le même client devait être limité: HTTP %d", got)
	}
	if got := apiRequest(server, "10.1.2.3:9999", "203.0.113.11").Code; got != http.StatusUnauthorized {
		t.Fatalf("un autre client derrière le proxy devait passer: HTTP %d", got)
	}
}

// Un client qui forge une chaîne X-Forwarded-For ne peut pas se cacher
// derrière une IP de proxy.
func TestForwardedForUsesTheFirstUntrustedHopFromTheRight(t *testing.T) {
	server := New(Config{
		APIToken:         "api",
		APIRateLimit:     1,
		APIRateBurst:     1,
		APIMaxConcurrent: -1,
		TrustedProxies:   []string{"10.0.0.0/8"},
	})
	// Le client prétend être 203.0.113.99 ; le proxy a ajouté sa vraie IP.
	if got := apiRequest(server, "10.1.2.3:9999", "203.0.113.99, 203.0.113.10").Code; got != http.StatusUnauthorized {
		t.Fatalf("première requête: HTTP %d", got)
	}
	if got := apiRequest(server, "10.1.2.3:9999", "203.0.113.98, 203.0.113.10").Code; got != http.StatusTooManyRequests {
		t.Fatalf("la vraie IP devait être limitée malgré l'en-tête forgé: HTTP %d", got)
	}
}

func TestConcurrencyLimiter(t *testing.T) {
	limiter := newAPILimiter(0, 0, 1)
	if limiter == nil {
		t.Fatal("un limiteur de concurrence était attendu")
	}
	if !limiter.acquire() {
		t.Fatal("la première place devait être libre")
	}
	if limiter.acquire() {
		t.Fatal("la seconde place devait être refusée")
	}
	limiter.release()
	if !limiter.acquire() {
		t.Fatal("la place libérée devait être reprise")
	}
	limiter.release()
}

func TestRateLimiterDefaultBurst(t *testing.T) {
	limiter := newAPILimiter(120, 0, -1)
	allowed := 0
	for index := 0; index < 100; index++ {
		if ok, _ := limiter.allow("ip:test"); ok {
			allowed++
		}
	}
	// Un quart de 120, et la première consommation part du seau plein.
	if allowed < 1 || allowed > 30 {
		t.Fatalf("rafale initiale inattendue: %d", allowed)
	}
}

func TestDisabledLimiterAllowsEverything(t *testing.T) {
	if limiter := newAPILimiter(0, 0, 0); limiter != nil {
		t.Fatal("aucune limite ne devait être active")
	}
	server := New(Config{APIToken: "api", APIRateLimit: -1, APIMaxConcurrent: -1})
	if server.limiter != nil {
		t.Fatal("les limites négatives doivent désactiver le limiteur")
	}
	for index := 0; index < 10; index++ {
		if got := apiRequest(server, "198.51.100.1:1111", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("requête %d: HTTP %d", index, got)
		}
	}
}

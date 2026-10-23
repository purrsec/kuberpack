package control

import (
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Les valeurs par défaut sont volontairement larges : elles protègent d'un
// client qui boucle ou d'une tentative de force brute, pas d'un tableau de
// bord qui interroge l'API toutes les quelques secondes. Un opérateur peut
// les régler par les options de `serve`.
const (
	// DefaultAPIRateLimit est le nombre de requêtes par minute et par client.
	DefaultAPIRateLimit = 1200
	// DefaultAPIMaxConcurrent est le nombre de requêtes simultanées.
	DefaultAPIMaxConcurrent = 128
)

// apiLimiter porte les deux limites de la surface HTTP : un seau de jetons par
// client et un nombre de requêtes simultanées.
//
// Aucune dépendance : le seau tient en quelques lignes, et un limiteur tiers
// n'apporterait pas la politique de clé (IP derrière un proxy de confiance).
type apiLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*rateBucket
	rate      float64 // jetons par seconde
	burst     float64
	now       func() time.Time
	lastSweep time.Time
	// concurrent borne les requêtes en vol. Les réponses de logs suivies
	// gardent leur place pendant leur durée : c'est voulu, elles sont déjà
	// limitées par le serveur.
	concurrent chan struct{}
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

// newAPILimiter construit les limites. Un taux nul désactive le seau, un
// nombre de connexions nul désactive la borne de concurrence ; nil signifie
// qu'aucune limite n'est active.
func newAPILimiter(perMinute, burst, maxConcurrent int) *apiLimiter {
	limiter := &apiLimiter{now: time.Now}
	if perMinute > 0 {
		limiter.rate = float64(perMinute) / 60
		if burst <= 0 {
			// Un quart de la limite par minute : une rafale courte passe, une
			// boucle ne tient pas.
			burst = perMinute / 4
			if burst < 1 {
				burst = 1
			}
		}
		limiter.burst = float64(burst)
		limiter.buckets = map[string]*rateBucket{}
	}
	if maxConcurrent > 0 {
		limiter.concurrent = make(chan struct{}, maxConcurrent)
	}
	if limiter.rate == 0 && limiter.concurrent == nil {
		return nil
	}
	return limiter
}

// allow consomme un jeton du client. Le second retour est l'attente conseillée
// avant de réessayer.
func (l *apiLimiter) allow(key string) (bool, time.Duration) {
	if l == nil || l.rate <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &rateBucket{tokens: l.burst, last: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.rate)
		bucket.last = now
	}
	if bucket.tokens < 1 {
		wait := time.Duration(math.Ceil((1-bucket.tokens)/l.rate*1000)) * time.Millisecond
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}
	bucket.tokens--
	return true, 0
}

// acquire prend une place de concurrence, ou refuse immédiatement.
func (l *apiLimiter) acquire() bool {
	if l == nil || l.concurrent == nil {
		return true
	}
	select {
	case l.concurrent <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *apiLimiter) release() {
	if l == nil || l.concurrent == nil {
		return
	}
	<-l.concurrent
}

// sweepLocked oublie les seaux revenus pleins et inactifs. Une carte non
// bornée serait une fuite mémoire offerte par un client qui change d'IP.
func (l *apiLimiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < 5*time.Minute {
		return
	}
	l.lastSweep = now
	for key, bucket := range l.buckets {
		if now.Sub(bucket.last) > 10*time.Minute && bucket.tokens >= l.burst {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) > 16384 {
		for key := range l.buckets {
			delete(l.buckets, key)
			if len(l.buckets) <= 8192 {
				break
			}
		}
	}
}

// guard applique les limites avant le routeur. /healthz reste hors limite :
// une sonde de readiness qui reçoit 429 ferait redémarrer le control plane.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || s.limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !s.limiter.acquire() {
			writeRetryAfter(w, time.Second)
			return
		}
		defer s.limiter.release()
		if ok, wait := s.limiter.allow(s.clientKey(r)); !ok {
			writeRetryAfter(w, wait)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeRetryAfter(w http.ResponseWriter, wait time.Duration) {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

// clientKey identifie le client : son IP, ou l'IP réelle derrière un proxy de
// confiance. L'API n'a qu'un token administrateur : une limite par token
// serait une limite globale, pas une limite par client.
func (s *Server) clientKey(r *http.Request) string {
	return "ip:" + s.clientIP(r)
}

func (s *Server) clientIP(r *http.Request) string {
	remote := remoteIP(r.RemoteAddr)
	if len(s.trusted) == 0 {
		return remote
	}
	addr, err := netip.ParseAddr(remote)
	if err != nil || !s.isTrusted(addr) {
		// La connexion ne vient pas d'un proxy déclaré : son en-tête
		// X-Forwarded-For serait une valeur choisie par le client.
		return remote
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(hops) - 1; index >= 0; index-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[index]))
		if err != nil {
			continue
		}
		hop = hop.Unmap()
		if !s.isTrusted(hop) {
			return hop.String()
		}
	}
	return remote
}

func (s *Server) isTrusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range s.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

// parseTrustedProxies lit les CIDR des proxys dont X-Forwarded-For est digne
// de confiance. Une entrée invalide est ignorée avec un message : elle ne doit
// pas élargir la confiance.
func parseTrustedProxies(entries []string) []netip.Prefix {
	var out []netip.Prefix
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				log.Printf("kuberpack: proxy de confiance %q ignoré: %v", entry, err)
				continue
			}
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			log.Printf("kuberpack: proxy de confiance %q ignoré: %v", entry, err)
			continue
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out
}

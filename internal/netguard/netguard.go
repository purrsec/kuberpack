// Package netguard applique une politique d'accès réseau sortant au control
// plane.
//
// Le control plane contacte des adresses qui viennent d'un manifeste : le
// registre d'une image déclarée, l'URL de clone rendue par une forge, le
// service de jeton annoncé par un registre. Une origine publique ne doit pas
// devenir un chemin vers la boucle locale, le service de métadonnées du
// fournisseur, les plages privées du cluster ou un service interne. La
// politique refuse ces adresses et ces noms par défaut ; une allowlist
// explicite (KUBERPACK_EGRESS_ALLOW, --egress-allow) autorise un registre ou
// une forge internes.
//
// La vérification qui compte est celle de l'IP réellement dialée : le
// DialContext résout le nom, filtre les adresses interdites puis diale l'IP
// choisie. Une réponse DNS qui change entre la vérification et la connexion
// (DNS rebinding) ne peut donc pas faire dialer une adresse refusée.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// EnvAllow est la variable qui porte l'allowlist des accès sortants.
// Elle contient des hôtes, des adresses ou des CIDR séparés par des virgules.
const EnvAllow = "KUBERPACK_EGRESS_ALLOW"

// MaxRedirects borne les redirections d'un client HTTP gardé. Une redirection
// est une nouvelle requête : elle est revérifiée comme la première.
const MaxRedirects = 3

// ErrBlocked identifie un refus de la politique d'egress. Les erreurs qui
// l'enveloppent disent quelle adresse ou quel nom a été refusé.
var ErrBlocked = errors.New("accès sortant interdit")

// Policy est une politique d'egress immuable après construction. Les méthodes
// de lecture sont sûres en concurrence.
type Policy struct {
	// hosts autorise exactement un nom (« registre.interne exemple »).
	hosts map[string]struct{}
	// suffixes autorise un nom et ses sous-domaines (entrée « .exemple.fr »).
	suffixes []string
	// prefixes autorise une adresse ou un CIDR, y compris privé.
	prefixes []netip.Prefix
}

// New crée une politique sans allowlist : seules les adresses publiques sont
// autorisées.
func New() *Policy {
	return &Policy{hosts: map[string]struct{}{}}
}

// Parse construit une politique à partir d'entrées d'allowlist.
//
// Une entrée est un nom d'hôte, un nom de sous-domaine (« *.exemple.fr » ou
// « .exemple.fr »), une adresse IP ou un CIDR. Une entrée invalide est une
// erreur : une faute de frappe ne doit pas élargir silencieusement la
// politique.
func Parse(entries []string) (*Policy, error) {
	policy := New()
	for _, entry := range entries {
		if err := policy.add(entry); err != nil {
			return nil, err
		}
	}
	return policy, nil
}

// FromEnvironment lit KUBERPACK_EGRESS_ALLOW.
//
// Une allowlist illisible ne désactive pas la garde : la politique repart
// vide, donc restrictive, et l'erreur est journalisée.
func FromEnvironment() *Policy {
	policy, err := Parse(strings.Split(os.Getenv(EnvAllow), ","))
	if err != nil {
		log.Printf("netguard: %s invalide: %v; aucune entrée retenue", EnvAllow, err)
		return New()
	}
	return policy
}

// TrustHosts retourne une copie de la politique qui autorise en plus les hôtes
// donnés. Elle sert aux adresses configurées par l'opérateur (forge, registre,
// dépôt GitOps), qui ne viennent pas d'un manifeste.
func (p *Policy) TrustHosts(hosts ...string) *Policy {
	out := p.clone()
	for _, host := range hosts {
		if err := out.add(host); err != nil {
			log.Printf("netguard: hôte de confiance %q ignoré: %v", host, err)
		}
	}
	return out
}

// TrustURLs autorise l'hôte des URL données. Une valeur sans schéma est lue
// comme un hôte (« ghcr.io »).
func (p *Policy) TrustURLs(raws ...string) *Policy {
	out := p.clone()
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		host := raw
		if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
			host = parsed.Host
		}
		if err := out.add(host); err != nil {
			log.Printf("netguard: URL de confiance %q ignorée: %v", raw, err)
		}
	}
	return out
}

// CheckHost vérifie un hôte (nom, adresse ou hôte:port).
func (p *Policy) CheckHost(host string) error {
	host = canonicalHost(host)
	if host == "" {
		return fmt.Errorf("%w: hôte vide", ErrBlocked)
	}
	if name, port, err := net.SplitHostPort(host); err == nil && port != "" {
		host = canonicalHost(name)
	}
	if p.hostAllowed(host) {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return p.checkAddr(addr)
	}
	if nameProhibited(host) {
		return fmt.Errorf("%w: le nom %q désigne le cluster ou les métadonnées", ErrBlocked, host)
	}
	return nil
}

// CheckURL vérifie le schéma et l'hôte d'une URL. La résolution DNS et le
// filtrage des IP se font au dial, pas ici.
func (p *Policy) CheckURL(target *url.URL) error {
	if target == nil {
		return fmt.Errorf("%w: URL vide", ErrBlocked)
	}
	scheme := strings.ToLower(target.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: schéma %q refusé (HTTP ou HTTPS attendu)", ErrBlocked, target.Scheme)
	}
	if target.Hostname() == "" {
		return fmt.Errorf("%w: URL sans hôte", ErrBlocked)
	}
	if target.User != nil {
		return fmt.Errorf("%w: identifiants inclus dans l'URL", ErrBlocked)
	}
	return p.CheckHost(target.Hostname())
}

// CheckRemoteURL vérifie une URL de dépôt avant de lancer Git.
//
// Une valeur sans schéma est un chemin local : c'est ce que les fixtures et un
// GitOps local utilisent. Git dialera lui-même ; la vérification DNS de cette
// fonction réduit la fenêtre de rebinding sans pouvoir la fermer, car Git ne
// passe pas par notre DialContext.
func (p *Policy) CheckRemoteURL(ctx context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w: URL distante vide", ErrBlocked)
	}
	target, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL distante %q: %w", raw, err)
	}
	if target.Scheme == "" {
		return nil
	}
	if err := p.CheckURL(target); err != nil {
		return err
	}
	host := canonicalHost(target.Hostname())
	if p.hostAllowed(host) {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		// CheckURL a déjà vérifié l'adresse littérale.
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return fmt.Errorf("résolution de %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %s ne se résout sur aucune adresse", ErrBlocked, host)
	}
	for _, addr := range addrs {
		if err := p.checkAddr(addr); err != nil {
			return fmt.Errorf("%s se résout sur %s: %w", host, addr, err)
		}
	}
	return nil
}

// DialContext résout l'hôte, ne garde que les adresses autorisées et diale
// l'IP retenue. L'IP dialée est donc toujours celle qui a été vérifiée.
func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("adresse sortante %q: %w", address, err)
	}
	host = canonicalHost(host)
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	// Un hôte explicitement autorisé peut résoudre ce qu'il veut : c'est
	// l'opérateur qui l'a déclaré.
	if p.hostAllowed(host) {
		return dialer.DialContext(ctx, network, address)
	}

	var addrs []netip.Addr
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		addrs = []netip.Addr{addr}
	} else {
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("résolution de %s: %w", host, err)
		}
	}
	allowed := p.allowedAddrs(addrs, network)
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%w: %s n'a aucune adresse autorisée", ErrBlocked, host)
	}
	var lastErr error
	for _, addr := range allowed {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("connexion à %s: %w", host, lastErr)
}

// HTTPClient retourne un client dont chaque requête et chaque redirection sont
// vérifiées, et dont les connexions passent par DialContext.
func (p *Policy) HTTPClient(timeout time.Duration) *http.Client {
	base := &http.Transport{
		// Un proxy configuré par l'opérateur relaie la destination : la
		// vérification des IP ne s'applique qu'à la connexion au proxy. Le
		// proxy doit alors appliquer sa propre politique de sortie.
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           p.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: guardedTransport{base: base, policy: p},
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("%w: plus de %d redirections", ErrBlocked, MaxRedirects)
			}
			return p.CheckURL(request.URL)
		},
	}
}

// guardedTransport vérifie l'URL avant de confier la requête au transport.
// Un service de jeton annoncé par un registre, par exemple, ne peut pas être
// une adresse interne.
type guardedTransport struct {
	base   http.RoundTripper
	policy *Policy
}

func (transport guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil {
		return nil, fmt.Errorf("%w: requête sans URL", ErrBlocked)
	}
	if err := transport.policy.CheckURL(request.URL); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

// allowedAddrs garde les adresses autorisées de la famille demandée.
func (p *Policy) allowedAddrs(addrs []netip.Addr, network string) []netip.Addr {
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		unmapped := addr.Unmap()
		if network == "tcp4" && !unmapped.Is4() {
			continue
		}
		if network == "tcp6" && !unmapped.Is6() {
			continue
		}
		if p.addrAllowed(unmapped) {
			out = append(out, unmapped)
		}
	}
	return out
}

func (p *Policy) checkAddr(addr netip.Addr) error {
	if !p.addrAllowed(addr) {
		return fmt.Errorf("%w: %s", ErrBlocked, addr)
	}
	return nil
}

// addrAllowed dit si une adresse peut être dialée : une entrée explicite de
// l'allowlist, ou une adresse publique sinon.
func (p *Policy) addrAllowed(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return !prohibited(addr)
}

func (p *Policy) hostAllowed(host string) bool {
	if _, ok := p.hosts[host]; ok {
		return true
	}
	for _, suffix := range p.suffixes {
		if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
			return true
		}
	}
	return false
}

func (p *Policy) clone() *Policy {
	out := New()
	for host := range p.hosts {
		out.hosts[host] = struct{}{}
	}
	out.suffixes = append(out.suffixes, p.suffixes...)
	out.prefixes = append(out.prefixes, p.prefixes...)
	return out
}

// add enregistre une entrée d'allowlist. Le format est volontairement strict :
// une entrée vide est ignorée, une entrée illisible est une erreur.
func (p *Policy) add(entry string) error {
	entry = canonicalHost(entry)
	if entry == "" {
		return nil
	}
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return fmt.Errorf("CIDR %q: %w", entry, err)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits())
		p.prefixes = append(p.prefixes, prefix.Masked())
		return nil
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		addr = addr.Unmap()
		p.prefixes = append(p.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		return nil
	}
	if strings.HasPrefix(entry, "*.") {
		entry = entry[1:]
	}
	if strings.HasPrefix(entry, ".") {
		if entry == "." {
			return fmt.Errorf("suffixe d'hôte %q invalide", entry)
		}
		p.suffixes = append(p.suffixes, entry)
		return nil
	}
	if host, port, err := net.SplitHostPort(entry); err == nil && port != "" {
		entry = canonicalHost(host)
		if entry == "" {
			return fmt.Errorf("hôte %q invalide", host)
		}
	}
	p.hosts[entry] = struct{}{}
	return nil
}

// canonicalHost normalise un hôte : minuscules, sans point final, sans
// crochets IPv6.
func canonicalHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}

// nameProhibited refuse les noms qui désignent, par convention, la machine
// locale, le cluster ou le service de métadonnées du fournisseur.
func nameProhibited(host string) bool {
	for _, name := range reservedNames {
		if host == name {
			return true
		}
	}
	for _, suffix := range reservedSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

var reservedNames = []string{
	"localhost",
	"metadata",
	"metadata.google.internal",
	"instance-data",
	"instance-data.ec2.internal",
	"kubernetes.default",
	"kubernetes.default.svc",
}

var reservedSuffixes = []string{
	".internal",
	".local",
	".localhost",
	".localdomain",
	".home.arpa",
	".cluster.local",
	".svc",
	".metadata",
}

// prohibited couvre les adresses qui n'ont pas de sens pour une origine
// publique : boucle locale, link-local (dont 169.254.169.254), plages privées
// RFC1918 et ULA IPv6, multicast, CGNAT et les préfixes réservés.
func prohibited(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, prefix := range prohibitedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

var prohibitedPrefixes = []netip.Prefix{
	mustPrefix("0.0.0.0/8"),       // « ce réseau »
	mustPrefix("100.64.0.0/10"),   // CGNAT, dont certains services de métadonnées
	mustPrefix("192.0.0.0/24"),    // IETF
	mustPrefix("192.0.2.0/24"),    // documentation
	mustPrefix("198.18.0.0/15"),   // benchmarks
	mustPrefix("198.51.100.0/24"), // documentation
	mustPrefix("203.0.113.0/24"),  // documentation
	mustPrefix("240.0.0.0/4"),     // réservé
	mustPrefix("::/96"),           // IPv4-compatible, déprécié
	mustPrefix("64:ff9b::/96"),    // NAT64
	mustPrefix("64:ff9b:1::/48"),  // NAT64 local
	mustPrefix("2001::/32"),       // Teredo
	mustPrefix("2001:db8::/32"),   // documentation
	mustPrefix("2002::/16"),       // 6to4
}

func mustPrefix(raw string) netip.Prefix {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		panic("netguard: préfixe invalide " + raw + ": " + err.Error())
	}
	return prefix
}

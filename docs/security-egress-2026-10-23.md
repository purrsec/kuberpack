# Durcissement des accès sortants et de la surface API — 23 octobre 2026

Cette passe traite le risque 8 de l’[audit](security-audit-2026-10-23.md) : une
référence de registre ou une URL de dépôt ne doit pas devenir un chemin vers la
boucle locale, le service de métadonnées du fournisseur, une plage privée du
cluster ou un service interne. Elle ajoute aussi des limites à la surface HTTP
et une NetworkPolicy d’egress optionnelle pour le control plane.

Périmètre : `internal/netguard` (nouveau), `internal/oci`, `internal/fetch`,
`internal/release`, `internal/promote`, `internal/control` et `cmd/kuberpack`.
Les preuves sont des tests locaux avec des serveurs HTTP et des dépôts Git
temporaires. Aucun VPS n’a été contacté.

## Politique SSRF

`internal/netguard` refuse par défaut :

- les adresses de boucle locale (`127.0.0.0/8`, `::1`), link-local
  (`169.254.0.0/16`, dont `169.254.169.254`, `fe80::/10`), multicast,
  non spécifiées et les plages privées RFC1918 (`10/8`, `172.16/12`,
  `192.168/16`) et ULA IPv6 (`fc00::/7`) ;
- les préfixes réservés ou de traduction : `0.0.0.0/8`, `100.64.0.0/10`
  (CGNAT, qui couvre certains services de métadonnées), `192.0.0.0/24`,
  `198.18.0.0/15`, `240.0.0.0/4`, blocs de documentation, `64:ff9b::/96`
  (NAT64), `2001::/32` (Teredo), `2002::/16` (6to4) ;
- les noms `localhost`, `metadata`, `metadata.google.internal`,
  `instance-data`, `kubernetes.default(.svc)` et les suffixes `.internal`,
  `.local`, `.localhost`, `.localdomain`, `.home.arpa`, `.cluster.local`,
  `.svc`, `.metadata` ;
- les URL qui ne sont pas HTTP(S), qui portent des identifiants inclus ou qui
  n’ont pas d’hôte.

Les adresses IPv4 encapsulées (`::ffff:127.0.0.1`) sont dé-encapsulées avant
le contrôle ; la casse et le point final d’un nom ne contournent pas les
refus.

### L’IP dialée est celle qui a été vérifiée

Les clients HTTP du registre et des tokens utilisent un `DialContext`
contrôlé : le nom est résolu au moment de la connexion, seules les adresses
autorisées sont conservées, puis l’IP retenue est dialée directement. Une
réponse DNS qui changerait entre la vérification et la connexion (DNS
rebinding) ne peut donc pas faire joindre une adresse privée.

Chaque requête est revérifiée par un `RoundTripper` gardé, et chaque
redirection (au plus 3) repasse par le contrôle. Une redirection vers
`169.254.169.254` ou un nom `.internal` est refusée. Le service de jeton
annoncé par un registre (`Www-Authenticate: Bearer realm=...`) est une nouvelle
destination : il est gardé comme le registre lui-même.

Le clone Git ne passe pas par ce `DialContext` : `fetch.Checkout` vérifie le
nom, l’adresse littérale et la résolution DNS avant de lancer Git, et Git est
configuré avec `http.followRedirects=false` et `protocol.ext.allow=never`.
Cette vérification préalable laisse une fenêtre de rebinding entre la
résolution et la connexion de Git ; elle est réduite par l’egress réseau
(NetworkPolicy du builder, et celle du control plane ci-dessous).

### Allowlist explicite

Une installation qui héberge sa forge, son registre ou son GitOps sur une
adresse privée doit la déclarer. Le format est un nom d’hôte, un suffixe
(`*.exemple.fr` ou `.exemple.fr`), une IP ou un CIDR, séparés par des virgules.

| Mécanisme | Valeur |
| --- | --- |
| `serve` | `--egress-allow` ou `KUBERPACK_EGRESS_ALLOW` |
| `trigger` | `KUBERPACK_EGRESS_ALLOW` |
| Chart | `egressAllow: "10.0.0.0/8,registry.interne"` |

Les adresses configurées par l’opérateur (`--git-url`, `--registry-url`,
`--gitops-url`, `KUBERPACK_BUILDS_GIT_URL`) sont ajoutées automatiquement à la
politique du control plane : une forge ou un registre interne n’a pas à être
déclaré deux fois. La même allowlist est transmise au Job du builder
(`KUBERPACK_EGRESS_ALLOW`), qui clone le dépôt de l’application avec la même
politique. Une entrée d’allowlist invalide fait échouer `serve` au démarrage ;
dans une bibliothèque, elle est journalisée et la politique reste restrictive.

Exemples :

```sh
# Registre Harbor interne et forge interne.
KUBERPACK_EGRESS_ALLOW=10.20.0.0/16,harbor.interne,git.interne

# Un dépôt GitOps local n’est pas concerné : un chemin sans schéma reste
# accepté, comme avant, pour les fixtures et un GitOps monté sur disque.
```

L’allowlist ne permet pas d’élargir les noms réservés au travers d’un
suffixe global (`*`) : chaque entrée est explicite.

## Limites de la surface API

`serve` borne désormais les requêtes avant le routeur :

| Option | Variable | Défaut |
| --- | --- | --- |
| `--api-rate-limit` | `KUBERPACK_API_RATE_LIMIT` | 1200 requêtes/minute/client |
| `--api-rate-burst` | `KUBERPACK_API_RATE_BURST` | quart de la limite |
| `--api-max-concurrency` | `KUBERPACK_API_MAX_CONCURRENCY` | 128 requêtes simultanées |
| `--trusted-proxies` | `KUBERPACK_TRUSTED_PROXIES` | vide (X-Forwarded-For ignoré) |

Une valeur négative désactive la limite correspondante. Le chart expose ces
réglages sous `apiLimits.ratePerMinute`, `apiLimits.burst`,
`apiLimits.maxConcurrency` et `apiLimits.trustedProxies`. Un refus est un
`HTTP 429` avec `Retry-After`. `/healthz` est exclu : une sonde qui reçoit 429
ferait redémarrer le control plane. Les corps JSON et webhook restent bornés à
1 Mio + 1 octet, comme avant.

La clé de la limite est l’IP de connexion. Derrière un ingress, renseigner le
CIDR de ses pods dans `trustedProxies` : l’IP réelle est alors le premier saut
non fiable en partant de la droite de `X-Forwarded-For`, ce qui empêche un
client de se cacher derrière une valeur forgée. Sans proxy déclaré, l’en-tête
est ignoré.

L’API n’a qu’un token administrateur : une limite « par token » serait une
limite globale. La limite par IP est donc la seule qui protège réellement les
tentatives d’authentification. Les webhooks restent protégés par leur signature
HMAC.

La concurrence des builds n’est pas modifiée : le worker traite les livraisons
une par une (`drain` séquentiel), donc un seul build à la fois. La limite de
concurrence HTTP ne compte pas les builds, qui ne sont pas des requêtes.

## NetworkPolicy d’egress du control plane

`charts/kuberpack/templates/controlplane-networkpolicy.yaml` est **désactivée
par défaut** (`controlPlaneNetworkPolicy.enabled: false`). Le control plane
doit joindre :

- le DNS du cluster ;
- l’API Kubernetes, souvent une `ClusterIP` dans un CIDR de services privé ;
- les kubelets, sur les IP privées des nœuds ;
- Infisical, dans son namespace interne ;
- la forge, le registre et le dépôt GitOps, qui peuvent être publics ou
  internes.

Une politique par défaut qui n’autoriserait que le public casserait la
réconciliation d’une installation standard. L’activer demande donc de lister
les CIDR internes de l’installation :

```yaml
controlPlaneNetworkPolicy:
  enabled: true
  allowedCIDRs:
    - 10.43.0.0/16     # services (API server)
    - 10.42.0.0/16     # pods (Infisical, ingress)
    - 192.168.1.0/24   # nœuds (kubelets) et forge interne
  allowedPorts: [443, 6443, 8080, 10250]
  extraEgress: []
```

Le DNS et le trafic public HTTP(S) sont déjà couverts par le template. Les
règles `extraEgress` sont ajoutées telles quelles pour un cas particulier
(namespaceSelector d’un opérateur, par exemple). La NetworkPolicy du namespace
de build reste active par défaut et exclut déjà les plages privées pour les
Jobs de build.

## Vérification reproductible

```sh
go test ./internal/netguard ./internal/oci ./internal/fetch ./internal/release \
        ./internal/promote ./internal/control ./cmd/kuberpack
go test -race ./internal/netguard ./internal/oci ./internal/control
helm template charts/kuberpack --set image=x --set gitURL=https://git.example.com \
     --set gitopsURL=https://git.example.com/o/r.git --set host=kuberpack.example.com
```

Tests représentatifs :

- `TestPolicyRefusesInternalAddresses`, `TestPolicyRefusesReservedNames`,
  `TestAllowlistAuthorizesPrivateDestinations`, `TestIPv4MappedAddressesAreUnmapped` ;
- `TestDialContextRefusesLoopbackByDefault`, `TestRedirectToMetadataIsRefused`,
  `TestRedirectsAreBounded`, `TestCheckRemoteURLRejectsPrivateAndMetadata` ;
- `TestResolveDigestRefusesLoopbackWithoutAllowlist`,
  `TestRegistryExchangeRefusesMetadataRealm`,
  `TestRegistryRefusesRedirectToMetadata` ;
- `TestCheckoutRefusesProhibitedRemoteBeforeGit` ;
- `TestAPIRateLimitIsPerClientIP`, `TestHealthzIsExemptFromRateLimit`,
  `TestForwardedForIsReadFromATrustedProxy`, `TestConcurrencyLimiter`.

## Limites et résidus

1. **Git n’est pas dialé par notre transport.** Le contrôle de `fetch.Checkout`
   est préalable (nom, IP littérale, DNS) ; un rebinding entre cette résolution
   et la connexion de Git reste théoriquement possible. La parade complète est
   l’egress réseau : NetworkPolicy du builder pour les Jobs, NetworkPolicy
   optionnelle du control plane. Un proxy Git (`http.proxy`) configuré par
   l’opérateur aurait le même effet de bord.
2. **Le proxy HTTP de l’environnement.** Les clients gardés honorent
   `HTTP_PROXY`/`HTTPS_PROXY` ; l’IP vérifiée est alors celle du proxy, pas
   celle de la destination. Un proxy utilisé avec Kuberpack doit appliquer sa
   propre politique de sortie, et son adresse doit être dans l’allowlist si
   elle est privée.
3. **La NetworkPolicy du control plane n’est pas active par défaut.** Elle est
   fournie et documentée, pas déployée : les CIDR internes dépendent de chaque
   installation. Tant qu’elle est désactivée, la protection réseau du control
   plane repose sur la politique applicative ci-dessus.
4. **Pas de limite par utilisateur.** L’API garde un token administrateur
   global ; la limite par IP ne crée pas d’identité. Un attaquant qui change
   d’IP peut contourner le seau, et un client légitime derrière un proxy non
   déclaré partage le seau de l’ingress.
5. **Les clients des forges (Forgejo, GitHub, GitLab) ne sont pas gardés par
   `netguard`.** Leur URL vient de la configuration de l’opérateur, pas d’un
   manifeste. Les garder demanderait de propager la politique dans
   `internal/git` et `internal/forgejo`.
6. **Aucun audit mémoire.** Cette passe ne démontre rien sur les binaires Git,
   le runtime Go ou les images tierces.

# Déploiement actuel

Cette page décrit **une** installation. Ce n’est pas le contrat produit.

| Rôle produit | Instance |
| --- | --- |
| k3s control plane + builder | homelab, nœud mini-PC, namespaces `kuberpack` et `kuberpack-build` |
| k3s production | VPS, GitOps Flux |
| Forgejo / registre | `git.host.bzh` |
| Dépôt GitOps | `pepe/infra-homelab` |
| Secrets | Infisical |
| Ingress | Traefik, `*.host.bzh` |

Hello-world : Flask Railpack sur `hello-world.host.bzh`, pin `sha-<commit>@digest`, chart `stateless`. Wattchman : Vite/Caddy Railpack sur `test-stagging.host.bzh` (HelmRelease `apps/wattchman-website`) ; `wattchman.fr` reste Railway. Flux Image Automation et les commentaires `$imagepolicy` restent éteints. Renovate ne touche pas ces images.

## Image Kuberpack

Control plane : Chainguard `git` (Wolfi), plus binaires statiques (`kuberpack`, `railpack`, `buildctl`, `uv`). Pas de Debian, pas de `buildkitd`, pas de Docker privilégié. `skopeo` n’est pas dans l’image (le binaire ne l’appelle pas).

Image control plane : `ghcr.io/purrsec/kuberpack:latest` (plus `sha-<commit>`). Le GitOps pin le digest : `ghcr.io/purrsec/kuberpack:latest@sha256:…`. Renovate met à jour le digest quand `latest` bouge.

```bash
docker build -t ghcr.io/purrsec/kuberpack:dev .
docker run --rm -p 8080:8080 \
  -e FORGEJO_TOKEN \
  -e KUBERPACK_API_TOKEN \
  -e KUBERPACK_GITOPS_URL \
  ghcr.io/purrsec/kuberpack:dev
```

Une installation neuve se fait avec `helmfile apply` (chart `charts/kuberpack`). Ne pas coller Kuberpack dans le Kustomization mini-pc `wait: true`. L’instance homelab est `kubernetes/kuberpack` réconcilié par Flux `kuberpack` (`wait: false`). Ingress `https://kuberpack.host.bzh/`, webhook `https://kuberpack.host.bzh/hooks/forgejo`. API : `Authorization: Bearer` (`KUBERPACK_API_TOKEN`). Pas d’Authelia (HMAC + bearer). Images : `ghcr.io/purrsec/kuberpack` et `ghcr.io/purrsec/kuberpack-builder`, pin digest.

Tokens builder dédiés (Infisical → Secret `kuberpack-builder`) : `kuberpack-builder-git-token` (clone HTTPS des apps / archive git) et `kuberpack-builder-registry-token` (`write:package`). Ce ne sont pas le PAT du control plane. Kuberpack ne pose pas de clé SSH : le builder clone en HTTPS avec ce jeton.

### Le builder ne doit pas avoir le jeton du control plane

Le builder **exécute le code du build**, et ce code n’est pas fiable. S’il porte le jeton du control plane, un build compromis peut réécrire le dépôt GitOps : il change alors ce qui est déployé.

La plateforme **détecte** ce cas. Le control plane ne lit aucun secret — il ne peut pas comparer lui-même — il donne donc au builder l’empreinte de son jeton, et le builder compare. Une empreinte ne se retourne pas.

- Par défaut, c’est un **avertissement** dans le log du build.
- `KUBERPACK_STRICT_CREDENTIALS=true` sur le control plane : le build est **refusé**, et le message dit quoi faire.

Les identifiants du builder, dans le Secret `kuberpack-builder` du namespace `kuberpack-build` :

| clé | portée |
|---|---|
| `git-token` (ou `kuberpack-builder-git-token`) | lecture seule sur les dépôts des applications |
| `registry-token` (ou `kuberpack-builder-registry-token`) | `write:package` sur le registre, rien d’autre |

Sur GitHub, un jeton fin (*fine-grained*) porte ces deux portées : l’un sur les dépôts des applications, en lecture ; l’autre sur le paquet du registre, en écriture. Le control plane garde, lui, un jeton qui écrit **uniquement** sur le dépôt GitOps.

Le Secret est monté dans le Job sous `/secrets`. Les fichiers sont nommés d’après les clés du Secret : le builder accepte `git-token` **ou** `kuberpack-builder-git-token`, et `registry-token` **ou** `kuberpack-builder-registry-token` (idem `builds-token` pour l’archive). Un Secret qui n’expose aucune de ces clés fait échouer le build sur `auth`.

### Accès sortants et limites API

Le control plane refuse par défaut un registre ou un dépôt hébergé sur une
adresse privée, la boucle locale ou le service de métadonnées. Une forge, un
registre ou un GitOps interne se déclare avec `egressAllow` (ou
`KUBERPACK_EGRESS_ALLOW` / `--egress-allow`) ; les URL configurées
(`gitURL`, `registryURL`, `gitopsURL`) sont ajoutées automatiquement.
L’allowlist est transmise au builder. Les limites HTTP se règlent dans
`apiLimits` (requêtes par minute et par IP, concurrence, proxys de confiance).
Détails et exemples : [Durcissement des accès sortants](security-egress-2026-10-23.md).

Archive de builds : `KUBERPACK_BUILDS_GIT_URL` (dépôt dédié, jamais une source Flux). Secret Flux vers Kuberpack : `KUBERPACK_FLUX_WEBHOOK_SECRET`. Previews : `KUBERPACK_PREVIEW_DOMAIN` (défaut `preview.host.bzh`). DNS apps : `KUBERPACK_DNS_TARGET` IPv6 seulement (AAAA). Une IPv4 dans cette variable est ignorée.

Secrets d’app (Stripe, `DATABASE_URL`, …) : Infisical (ou Secret Kubernetes) nommé `app-<name>` dans le namespace `apps`. Kuberpack ne sync pas Infisical ; il déclare seulement `envFrom`. Postgres et autres stateful : GitOps hors chart `stateless`, URL dans ce Secret.


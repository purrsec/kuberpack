# État du code (pas le contrat produit)

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `platform-deployer` : `promote` (pin `image` dans un values GitOps), `trigger` (clone SHA → `railpack prepare` → dispatch Forgejo), `serve` (`GET /healthz`).
- Chart Helm `charts/stateless` (Deployment, Service, Ingress, probes, optionnel NetworkPolicy).
- Client Forgejo : lecture repo/branche + `workflow_dispatch`.
- Cobaye : `pepe/hello-world` construit par `app-release.yaml` (k3s `app-builder`), déployé par Flux sur le VPS. URL : `https://hello-world.host.bzh/`.

## Pas fait

- Control plane HTTP (`POST /api/v1/apps`), SQLite, webhooks HMAC.
- `promote` branché après le digest du builder.
- Tag builder `sha-<commit>` (le runner homelab tague encore `main-<run>-<sha>`).
- Exécuteur `uv` (détection seulement ; le build passe par Railpack).
- Previews, image Kuberpack sur le mini-pc, Helm tests en prod.

## Hors arbre git

`kubero-main/` est un clone de référence, gitignoré. Pas du produit.

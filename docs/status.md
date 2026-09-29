# État du code (pas le contrat produit)

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger` (pipeline digest → GitOps), `serve` (control plane HTTP).
- `POST /api/v1/apps`, `GET /api/v1/apps`, webhook `POST /hooks/forgejo` (HMAC SHA-256, dédup, file SQLite).
- SQLite WAL + secrets HMAC en fichiers (`--data`). Pas de secret dans SQLite.
- Chart Helm `charts/stateless`.
- Client Forgejo : repo, branche, `workflow_dispatch`, création de webhook.
- Cobaye : `pepe/hello-world` sur `https://hello-world.host.bzh/` ; pin GitOps `sha-<commit>@digest` ; Flux a upgradé.
- Control plane sur le mini-pc : `https://kuberpack.host.bzh/` (`imagePullPolicy: Never`, Flux enfant `wait: false`).

## Pas fait

- Clé de deploy SSH read-only à l’enregistrement.
- Builder Kuberpack (le dispatch `railpack-release.sh` est un cobaye).
- Tag builder `sha-<commit>` poussé par le cobaye.
- Exécuteur `uv`, previews, Helm tests en prod.

## Hors arbre git

`kubero-main/` est un clone de référence, gitignoré. Pas du produit.

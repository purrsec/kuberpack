# État du code (pas le contrat produit)

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger` (pipeline digest → GitOps), `serve` (control plane HTTP).
- `POST /api/v1/apps`, `GET /api/v1/apps`, webhook `POST /hooks/forgejo` (HMAC SHA-256, dédup, file SQLite).
- SQLite WAL + secrets HMAC en fichiers (`--data`). Pas de secret dans SQLite.
- Chart Helm `charts/stateless`.
- Client Forgejo : repo, branche, `workflow_dispatch`, création de webhook.
- Cobaye : `pepe/hello-world` sur `https://hello-world.host.bzh/` ; pin GitOps `sha-<commit>@digest` ; Flux a upgradé.
- Control plane sur le mini-pc : `https://kuberpack.host.bzh/` ; la version actuellement déployée utilise encore Forgejo Actions. Flux enfant `wait: false`.
- Dans le code : builder Railpack en Job k3s avec sidecar BuildKit rootless, Trivy, Syft et push `sha-<commit>`. Activation conditionnée par `KUBERPACK_BUILDER_IMAGE` ; le déploiement actuel utilise encore Forgejo Actions.
- Test réel sur le mini-PC : Job `kuberpack-build-7fc1ad70955cc0e2` réussi pour `pepe/hello-world`, image de test publiée par le worker au digest `sha256:468915261411d8d2b7ad7e34393393de7a25477878a7cb541a513fc45a4398f1`. Un Pod tiré par ce digest a répondu `200` sur `/healthz`.

## Pas fait

- Clé de deploy SSH read-only à l’enregistrement.
- Mise en service GitOps du nouveau control plane et du builder : les manifests sont prêts dans `infra-homelab`, mais non poussés. Le control plane actuellement déployé n’a pas encore lancé de Job lui-même.
- Publication durable des SBOM Syft (aujourd’hui produits dans le Job puis perdus à sa fin).
- Exécuteur `uv`, previews, Helm tests en prod.

## Hors arbre git

`kubero-main/` est un clone de référence, gitignoré. Pas du produit.

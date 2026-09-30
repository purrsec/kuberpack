# État du code (pas le contrat produit)

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger` (pipeline digest → GitOps), `serve` (control plane HTTP), `builder` (worker du Job k3s).
- `POST /api/v1/apps`, `GET /api/v1/apps`, webhook `POST /hooks/forgejo` (HMAC SHA-256, dédup, file SQLite).
- SQLite WAL + secrets HMAC en fichiers (`--data`). Pas de secret dans SQLite.
- Chart Helm `charts/stateless`.
- Client Forgejo : repo, branche, création de webhook. Plus de `workflow_dispatch`.
- Control plane et builder déployés par Flux sur le mini-pc (`kubernetes/kuberpack`, Kustomization enfant `wait: false`). Ingress `https://kuberpack.host.bzh/`. Token API Infisical `KUBERPACK_TOKEN`. `KUBERPACK_BUILDER_IMAGE` est obligatoire.
- Builder Railpack en Job k3s (`kuberpack-build`), sidecar BuildKit rootless, Trivy, Syft, push `sha-<commit>`.
- `POST /api/v1/apps` écrit le contrat GitOps (`values.yaml`, HelmRelease, kustomization d’app). Flux n’est activé (entrée dans le kustomization parent) qu’après le premier digest. Ensuite seul `image` change. Les fichiers existants ne sont pas écrasés.
- Cobaye `pepe/hello-world` enregistré, webhook Forgejo vers `https://kuberpack.host.bzh/hooks/forgejo`.
- Trajet **webhook → control plane → Job → commit GitOps → Flux** prouvé : push `f10cf296` (`n: 4`) → Job `kuberpack-build-736ffeb3f6e1d4bb` Complete → pin `git.host.bzh/pepe/hello-world:sha-f10cf296ea2c210d374847d1368d0ef9c848664c@sha256:127130f2edce05a906342bba4700c43b9718a6fb81891b6af62398308d92de02` (commit GitOps `fc2727ab`) → HelmRelease `hello-world` v14 Ready → `https://hello-world.host.bzh/` répond `{"app":"flask-uv","n":4,"via":"kuberpack"}`, `/healthz` 200.
- Un Job antérieur lancé à la main (`kuberpack-build-7fc1ad70955cc0e2`) avait déjà construit et servi une image ; ce n’était pas le trajet webhook.

## Pas fait

- Déplacer `wattchman-website` (webhook et Deployment bruts encore sur le receiver cobaye) puis supprimer `railpack-release.sh`, `app-release.yaml` et `release-webhook.py`.
- Publication durable des SBOM Syft et des logs de build (produits dans le Job, perdus à sa fin / TTL 24 h).
- Tags durables pour les images Kuberpack (control plane et builder portent encore des tags `test-*`).
- Déployer une image Kuberpack qui contient ce code (l’enregistrement GitOps n’est pas encore live).
- Clé de deploy SSH read-only à l’enregistrement.
- Tests Helm en prod, previews, UX.
- Exécuteur `uv` dédié : Railpack construit déjà le cobaye Python avec `uv`.
- Tokens builder dédiés : les Jobs montent encore les secrets Infisical cobaye `app-builder-git-token` et `app-builder-registry-token`.

## Hors arbre git

`kubero-main/` est un clone de référence, gitignoré. Pas du produit.

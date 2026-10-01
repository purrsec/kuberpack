# État du code

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger`, `serve`, `builder`.
- HTTP : `POST/GET/PATCH/DELETE /api/v1/apps`, `GET /api/v1/apps/{name}/builds`, `POST /api/v1/apps/{name}/redeploy`, webhook Forgejo (`push` + `pull_request`), webhook Flux (`POST /hooks/flux`) pour `rolled_back`.
- SQLite WAL + secrets HMAC et clé SSH de deploy en fichiers (`--data`). Pas de secret dans SQLite.
- Chart Helm `charts/stateless` avec Helm test HTTP. Les HelmRelease générées ont `test.enable: true` et `upgrade.remediation.strategy: rollback`. Enregistrement prod : `envFrom` vers Secret `app-<name>` (l’admin le crée ; Kuberpack ne le remplit pas).
- Préviews `autodeploy_pr` : branche GitOps `previews`, release `<app>-pr-<n>`, hôte `pr-<n>.<app>.<preview-domain>`, forks refusés, GC TTL, statut Forgejo `kuberpack/preview`.
- Builder Railpack (Job k3s) : sidecar BuildKit rootless, Trivy, Syft, tag `sha-<commit>` uniquement. `uv` n’est pas un exécuteur séparé ; Railpack consomme `uv.lock`.
- Archive de build optionnelle : `KUBERPACK_BUILDS_GIT_URL` (no-op si vide). Ce dépôt ne doit jamais être une source Flux.
- Identité d’image : digest. GitOps ne réécrit que `image` + `track` humain.
- `track` : `main` suit les pushes ; un SHA gèle la prod et réutilise le digest publié.

## Pas fait / hors ce dépôt

- CLI/UI au-delà de HTTP (phase 5).
- `wattchman.fr` est encore Railway (hors kuberpack).
- UI de logs sur `serve` : hors produit.

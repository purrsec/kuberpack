# État du code

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger`, `serve`, `project create|delete`, `builder`.
- HTTP : `POST/GET/DELETE /api/v1/projects` (le manifeste est la seule voie de déclaration), `GET /api/v1/apps`, `GET /api/v1/apps/{name}`, `GET /api/v1/apps/{name}/builds`, `POST /api/v1/apps/{name}/redeploy`, webhook Forgejo (`push` + `pull_request`), webhook Flux (`POST /hooks/flux`) pour `rolled_back`.
- Manifeste `kuberpack.yaml` : source de vérité unique. Parsing **strict** (un champ inconnu est une erreur), refus explicites (`type` autre que `stateless`, addon autre que `cnpg`, `backup.s3`, peer inconnu). Les champs déclarés sont effectifs : `replicas`, `env`, `build.*`, `branch`, `strategy`, `policies.defaultDeny`, `backup.retention`, `namespace`.
- La compilation du manifeste arme le pipeline : app SQLite (`<projet>-<service>`), secret HMAC, webhook Forgejo, premier build, et `values_path` du service (le digest est épinglé dans le projet, pas dans l’ancien arbre `apps/`).
- Le `values.yaml` d’un service est régénéré à chaque compilation : le manifeste pilote le runtime, l’`image` et le `track` épinglés sont conservés.
- Flux : un `Kustomization` par arbre. Les projets (`kubernetes/vps/projects`) exigent leur propre `Kustomization` : sans lui, un projet compilé n’est jamais appliqué.
- SQLite WAL + secrets HMAC en fichiers (`--data`). Pas de secret dans SQLite. Pas de clé SSH : clone HTTPS par jeton (control plane et builder).
- Chart Helm `charts/stateless` avec Helm test HTTP. Les HelmRelease générées ont `test.enable: true` et `upgrade.remediation.strategy: rollback`. Enregistrement prod : `envFrom` vers Secret `app-<name>` (l’admin le crée ; Kuberpack ne le remplit pas).
- Préviews `autodeploy_pr` : branche GitOps `previews`, release `<app>-pr-<n>`, hôte `pr-<n>.<app>.<preview-domain>`, forks refusés, GC TTL, statut Forgejo `kuberpack/preview`.
- Builder Railpack (Job k3s) : sidecar BuildKit rootless, Trivy, Syft, tag `sha-<commit>` uniquement. `uv` n’est pas un exécuteur séparé ; Railpack consomme `uv.lock`.
- Archive de build optionnelle : `KUBERPACK_BUILDS_GIT_URL` (no-op si vide). Ce dépôt ne doit jamais être une source Flux.
- Identité d’image : digest. GitOps ne réécrit que `image` + `track` humain.
- `track` : `main` suit les pushes ; un SHA gèle la prod et réutilise le digest publié.
- ExternalDNS : `KUBERPACK_DNS_TARGET` filtré IPv6 (AAAA seulement).

## Pas fait / hors ce dépôt

- CLI/UI au-delà de HTTP (phase 5).
- `wattchman.fr` est encore Railway (hors kuberpack).
- UI de logs sur `serve` : hors produit.

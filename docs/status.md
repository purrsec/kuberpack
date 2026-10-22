# État du code

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

## Fait

- CLI `kuberpack` : `promote`, `trigger`, `serve`, `project create|delete`, `builder`.
- HTTP : `POST/GET/DELETE /api/v1/projects` (le manifeste est la seule voie de déclaration), `GET /api/v1/apps`, `GET /api/v1/apps/{name}`, `GET /api/v1/apps/{name}/builds`, `POST /api/v1/apps/{name}/redeploy`, webhook Forgejo (`push` + `pull_request`), webhook Flux (`POST /hooks/flux`) pour `rolled_back`.
- Manifeste `kuberpack.yaml` : source de vérité unique. Parsing **strict** (un champ inconnu est une erreur), refus explicites (`type` autre que `stateless`, addon autre que `cnpg`, `backup.s3`, peer inconnu). Les champs déclarés sont effectifs : `replicas`, `env`, `build.*`, `branch`, `strategy`, `policies.defaultDeny`, `backup.retention`, `namespace`.
- La compilation du manifeste arme le pipeline : app SQLite (`<projet>-<service>`), secret HMAC, webhook Forgejo, premier build, et `values_path` du service (le digest est épinglé dans le projet, pas dans l’ancien arbre `apps/`).
- Le `values.yaml` d’un service est régénéré à chaque compilation : le manifeste pilote le runtime, l’`image` et le `track` épinglés sont conservés.
- Addons : les moteurs sont pluggables (`AddonEngine` + un registre). Moteurs disponibles : `cnpg` (PostgreSQL, `DATABASE_URL` depuis le Secret généré par CNPG) et `valkey` (cache, `VALKEY_URL`). Un service déclare les addons qu’il utilise (`services.*.addons`) et reçoit leur environnement ; la `NetworkPolicy` les autorise.
- Le control plane écrit son chart dans le dépôt GitOps au démarrage : le chart appliqué est celui qu’il connaît (les valeurs qu’il écrit n’iraient pas dans un chart obsolète).
- La forge git est un **provider** (`internal/git`) : Forgejo, GitHub et GitLab sont implémentés, un seul registre les choisit (`KUBERPACK_GIT_PROVIDER`). Le registre d'images est configurable séparément de la forge (`KUBERPACK_REGISTRY_URL`).
- Deux jetons, séparés par principe : le jeton de **lecture** (control plane : cloner, lire, poser les webhooks) et le jeton de **build** (builder : pousser l'image). Voir [install.md](install.md).
- Une application **tierce** se déploie avec son URL de dépôt : `repository: https://github.com/miniflux/v2`. La forge de l'URL est déduite, le dépôt public est lu en anonyme, le webhook est impossible (un avertissement, pas une erreur), et l'image est publiée dans le registre de la plateforme.
- L'image est publiée sous le namespace du **plateforme** (`<registre>/<utilisateur>/<application>`), pas sous celui du dépôt : le code d'un tiers ne dicte pas où va l'image.
- L'addon PostgreSQL fournit aussi les variables discrètes (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_HOST`, `POSTGRES_PORT`) en plus de `DATABASE_URL` : une application tierce qui attend `POSTGRES_*` trouve sa base.
- Le `PATH` de l'image contient le dossier de l'application : la commande de démarrage nomme le binaire compilé (`miniflux`), sans chemin.
- Le test hook du chart dispose d'un budget court et le HelmRelease d'un timeout de 2 min : une application cassée ne tient pas le pipeline.
- Le dépôt applicatif est une source de code : Kuberpack n’y écrit aucun fichier, il ne le crée pas et n’y pousse pas de commit. Il le clone pour construire et y pose un webhook.
- Python : un `pyproject.toml` sans `uv.lock` (et sans `requirements.txt`) est **refusé** au build : Railpack n’y installerait rien et l’image démarrerait sans ses dépendances. Un build en échec avec un message clair vaut mieux qu’une image déployée qui crash.
- Le HelmRelease n’attend pas les pods (`disableWait`) : une image cassée ne bloque plus le déploiement suivant. La santé de l’application reste le rôle du test hook du chart, qui la signale sans bloquer.
- Webhook sur le dépôt GitOps : un push qui change un manifeste recompile le projet (le manifeste est lu depuis le dépôt, jamais réécrit). Le statut de commit sur le push dit si le manifeste compile.
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

- UI web : le CLI (`kuberpack apps`, `app`, `builds`, `logs`, `redeploy`, `project`) couvre le pilotage courant ; une interface web reste à faire.
- `wattchman.fr` est encore Railway (hors kuberpack).
- UI de logs sur `serve` : hors produit.

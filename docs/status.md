# État du code

Les autres pages de `docs/` décrivent **le produit visé**. Cette page décrit **ce qui est implémenté**.

L'[audit du 22 octobre](audit-2026-10-22.md) détaille les défauts constatés,
les corrections et les limites vérifiées. Le parcours développeur est
`project init` (fichier local), puis `project compile` (génération et push
par le control plane). Les services `image:` sont aussi visibles dans `apps`.
Les projets injectent uniquement leurs clés de secrets explicitement
déclarées, par des références obligatoires ; leur synchronisation Infisical
est générée dans le projet et appliquée avec lui.

`install` dispose désormais d'un questionnaire et d'étapes Go reprenables :
inspection, identifiants conservés, installation ou réutilisation de Flux et
des opérateurs, raccordement Infisical, puis control plane et vérification.
Le socle et les projets ont des périmètres GitOps dédiés. Les Helmfiles
indépendants restent gérés par Helmfile ; Kuberpack ne les exécute pas.
Les parcours réels sont validés sur k3s Docker et sur un VPS recréé de 8 Go,
avec certificats publics Let's Encrypt, secrets chargés par le CLI Infisical,
Stirling déclaré par un seul manifeste et réinstallation sans nouveau commit.
La vérification finale attend la configuration demandée à Flux avant de
contrôler la santé de la release.
Voir [installation](install.md), [validation Docker](install-validation-2026-10-22.md)
et [validation VPS](install-vps-2026-10-22.md).

La [passe de sécurité du 23 octobre](security-audit-2026-10-23.md) durcit
l’isolation réseau des services, la séparation des credentials, l’image du
control plane, l’admission des Jobs du builder et les entrées de l’API. Elle
est validée sur le banc Docker. Elle ne fait pas de Kuberpack une plateforme
multi-tenant : l’autorité GitOps du CP et le partage des identités Infisical
restent les risques critiques documentés dans cet audit.

## Fait

- CLI `kuberpack` : `install`, `promote`, `trigger`, `serve`, `project init|compile|create|delete`, `apps`, `app status|builds|build-logs|logs|redeploy`, `builder`.
- Logs d'exécution via `app logs`, pour les images publiées comme pour les apps construites : `-f`, `--tail`, `--since`, `--previous`, `--timestamps`, `-c`. Fusion des réplicas avec préfixe, appartenance des pods vérifiée et annulation du flux à la déconnexion. `app build-logs` conserve l'accès au builder, avec compatibilité de `app logs --build N`. Voir [commandes et limites](api.md#consulter-les-logs).
- Observation des déploiements via l'API : colonnes `STATUS`/`READY` dans `apps`, détail dans `app status`, sortie `--json`. Générations Deployment/Flux, réplicas, arrêt volontaire et erreurs du rollout courant sont distingués ; données indisponibles = `unknown`. Le namespace réel est conservé et les droits de lecture des workloads restent dans le périmètre du projet, sans lecture de Secrets. Voir [API et commandes](api.md#consulter-les-déploiements).
- Ressources dans `app status` : CPU/RAM via l'API Kubernetes, demandes et limites réelles, pourcentages, redémarrages et mesures par conteneur. Bridage CFS via HTTPS kubelet vérifié, corrélé à l'identifiant courant, affiché comme cumul depuis le démarrage. Sources absentes/périmées et couvertures partielles sont explicites ; aucune commande exécutée dans les pods. Droits cluster supplémentaires limités à `get nodes` et `get nodes/metrics`. Voir [contrat et limites](api.md#mesures-de-ressources-et-bridage-cpu).
- HTTP : `POST/GET/DELETE /api/v1/projects` (le manifeste est la seule voie de déclaration), `GET /api/v1/apps`, `GET /api/v1/apps/{name}`, `GET /api/v1/apps/{name}/builds`, `POST /api/v1/apps/{name}/redeploy`, webhook Forgejo (`push` + `pull_request`), webhook Flux (`POST /hooks/flux`) pour `rolled_back`.
- Manifeste `kuberpack.yaml` : source de vérité unique. Parsing **strict** (un champ inconnu est une erreur), refus explicites (`type` autre que `stateless`, moteur d’addon indisponible, peer inconnu, source absente ou ambiguë, ressources et volumes invalides). Les champs déclarés sont effectifs : `replicas`, `env`, `build.*`, `branch`, `strategy`, `policies.defaultDeny`, `backup.retention`, `namespace`.
- La compilation du manifeste arme le pipeline : app SQLite (`<projet>-<service>`), secret HMAC, webhook Forgejo, premier build, et `values_path` du service (le digest est épinglé dans le projet, pas dans l’ancien arbre `apps/`).
- Le `values.yaml` d’un service est régénéré à chaque compilation : le manifeste pilote le runtime, l’`image` et le `track` épinglés sont conservés.
- Addons : les moteurs sont pluggables (`AddonEngine` + un registre). Moteurs disponibles : `cnpg` (PostgreSQL), `valkey` (cache), `mongo`, `rustfs` (S3) et `volume` (disque, notamment SQLite). Les variables et volumes des dépendances sont fournis par leurs moteurs ; `backup.s3` peut référencer le stockage objet du projet. Un service déclare les addons qu’il utilise (`services.*.addons`) et reçoit leur environnement ; la `NetworkPolicy` les autorise.
- Le control plane écrit son chart dans le dépôt GitOps au démarrage : le chart appliqué est celui qu’il connaît (les valeurs qu’il écrit n’iraient pas dans un chart obsolète).
- La forge git est un **provider** (`internal/git`) : Forgejo, GitHub et GitLab sont implémentés, un seul registre les choisit (`KUBERPACK_GIT_PROVIDER`). Le registre d'images est configurable séparément de la forge (`KUBERPACK_REGISTRY_URL`).
- Les credentials sont séparés : écriture GitOps et API de forge, lecture des sources privées du builder, publication au registre et clé d’administration Kuberpack. L’installateur refuse les réutilisations ; les builds refusent par défaut un credential identique à celui du control plane. Voir [sécurité](security.md).
- Une application **tierce** se déploie avec son URL de dépôt : `repository: https://github.com/miniflux/v2`. La forge de l'URL est déduite, le dépôt public est lu en anonyme, le webhook est impossible (un avertissement, pas une erreur), et l'image est publiée dans le registre de la plateforme.
- L'image est publiée sous le namespace du **plateforme** (`<registre>/<utilisateur>/<application>`), pas sous celui du dépôt : le code d'un tiers ne dicte pas où va l'image.
- L'addon PostgreSQL fournit aussi les variables discrètes (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_HOST`, `POSTGRES_PORT`) en plus de `DATABASE_URL` : une application tierce qui attend `POSTGRES_*` trouve sa base.
- Le `PATH` de l'image contient le dossier de l'application : la commande de démarrage nomme le binaire compilé (`miniflux`), sans chemin.
- Le test HTTP et le timeout Helm suivent `startup_budget` ; un service à zéro réplique n’exécute pas de test HTTP. Les services avec disque utilisent `Recreate`, avec une migration compatible Helm 4 générée par la plateforme.
- Le dépôt applicatif est une source de code : Kuberpack n’y écrit aucun fichier, il ne le crée pas et n’y pousse pas de commit. Il le clone pour construire et y pose un webhook.
- Python : un `pyproject.toml` sans `uv.lock` (et sans `requirements.txt`) est **refusé** au build : Railpack n’y installerait rien et l’image démarrerait sans ses dépendances. Un build en échec avec un message clair vaut mieux qu’une image déployée qui crash.
- Le HelmRelease attend que la nouvelle révision soit prête avant le test HTTP. Son timeout suit le budget de démarrage ; Flux 2.8+ permet à une nouvelle correction d'interrompre l'attente de la révision précédente.
- Webhook sur le dépôt GitOps : un push qui change un manifeste recompile le projet (le manifeste est lu depuis le dépôt, jamais réécrit). Le statut de commit sur le push dit si le manifeste compile.
- Flux : un `Kustomization` par arbre. Les projets (`kubernetes/vps/projects`) exigent leur propre `Kustomization` : sans lui, un projet compilé n’est jamais appliqué.
- SQLite WAL + secrets HMAC en fichiers (`--data`). Pas de secret dans SQLite. Pas de clé SSH : clone HTTPS par jeton (control plane et builder).
- Chart Helm `charts/stateless` avec Helm test HTTP. Les HelmRelease générées activent le test pour les services non suspendus et utilisent `upgrade.remediation.strategy: rollback`. Les projets utilisent des `secretKeyRef` obligatoires pour les seules clés déclarées. Les anciens services autonomes conservent leur `envFrom` optionnel.
- Préviews `autodeploy_pr` : branche GitOps `previews`, release `<app>-pr-<n>`, hôte `pr-<n>.<app>.<preview-domain>`, forks refusés, GC TTL, statut Forgejo `kuberpack/preview`.
- Builder Railpack (Job k3s) : sidecar BuildKit rootless, Trivy, Syft, tag `sha-<commit>` uniquement. `uv` n’est pas un exécuteur séparé ; Railpack consomme `uv.lock`.
- Archive de build optionnelle : `KUBERPACK_BUILDS_GIT_URL` (no-op si vide). Ce dépôt ne doit jamais être une source Flux.
- Identité d’image : digest. GitOps ne réécrit que `image` + `track` humain.
- `track` : `main` suit les pushes ; un SHA gèle la prod et réutilise le digest publié.
- ExternalDNS : `KUBERPACK_DNS_TARGET` filtré IPv6 (AAAA seulement).

## Pas fait / hors ce dépôt

- UI web : le CLI (`kuberpack apps`, `app`, `builds`, `logs`, `redeploy`, `project`) couvre le pilotage courant ; une interface web reste à faire.
- Isolation multi-tenant : le CP peut encore écrire le dépôt qui pilote Flux, la clé API est administrateur global et les projets partagent le projet/chemin Infisical. L’ordre de travail est dans [l’audit sécurité](security-audit-2026-10-23.md).
- `wattchman.fr` est encore Railway (hors kuberpack).
- UI de logs sur `serve` : hors produit.

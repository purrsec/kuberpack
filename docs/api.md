# API

HTTP d’abord. CLI (`kuberpack`) ou UI ensuite. Pas d’exposition publique sans authentification.

## Créer une application

```http
POST /api/v1/apps
```

```json
{
  "name": "web",
  "repository": "org/web",
  "branch": "main",
  "strategy": "auto",
  "autodeploy": true,
  "autodeploy_pr": true,
  "hostname": "web.example.org",
  "port": 8080,
  "healthcheck": "/"
}
```

| Champ | Rôle |
| --- | --- |
| `name` | Identifiant plateforme et préfixe des releases |
| `repository` | `owner/name` Forgejo |
| `branch` | Branche dont les pushes deviennent la production |
| `strategy` | `auto`, `railpack` ou `uv` ; `auto` inspecte le commit avant le build |
| `autodeploy` | Push → build → promotion GitOps |
| `autodeploy_pr` | Previews |
| `hostname` | Ingress de production |
| `port` | Port réellement écouté par le processus |
| `healthcheck` | Chemin HTTP des probes et du Helm test |

Le port se configure, il ne se suppose pas. L’image peut écouter `80` ou `8080`. Une probe sur le mauvais port fait échouer la release.

## Overrides de build

La stratégie détecte langage et commandes. Les exceptions vivent dans Kuberpack :

```json
{
  "root_directory": "frontend",
  "build_command": "npm run build",
  "start_command": "npm run start"
}
```

Un `Procfile` est optionnel. Aucun fichier de plateforme n’est exigé dans le dépôt applicatif.

À l’enregistrement, Kuberpack matérialise ce JSON en YAML GitOps (`kubernetes/vps/apps/<name>/`). Ingress, pull secret, TLS et DNS viennent des défauts plateforme (`KUBERPACK_DNS_TARGET`, …), pas du dépôt app. Le `values.yaml` de production pose `envFrom.secretRef.name: app-<name>` ; les clés du Secret ne passent jamais par l’API. Flux ne voit l’app qu’après le premier pin `image`. Les apps déjà enregistrées gardent leur YAML : ajouter le `envFrom` à la main si besoin.

Le YAML GitOps est le contrat runtime. `track: main` (défaut, ou vide / `latest`) suit la branche de production. `track: <sha>` gèle la prod sur ce commit : les nouveaux pushes peuvent encore construire, mais ne changent plus `image`. Kuberpack repose alors le digest déjà publié (`sha-<commit>@sha256:…`), sans rebuild. `image:` reste un digest ; ce n’est pas un tag mutable.

## Suite HTTP

`PATCH /api/v1/apps/{name}` — champs optionnels : `branch`, `strategy`, `autodeploy`, `autodeploy_pr`, `hostname`, `port`, `healthcheck`, `start_command`. Ne réécrit pas GitOps (`image` reste le seul champ que le bot change).

`DELETE /api/v1/apps/{name}` — retire le webhook, la clé HMAC, l’entrée SQLite, et enlève l’app du kustomization parent (Flux prune).

`GET /api/v1/apps/{name}/builds` — historique SQLite (`status` : `running`, `succeeded`, `failed`, `rolled_back`).

`POST /api/v1/apps/{name}/redeploy` — enfile un build du HEAD courant.

`POST /hooks/flux` — notification HelmRelease en erreur → `rolled_back` + statut Forgejo `warning`. Secret `KUBERPACK_FLUX_WEBHOOK_SECRET` (HMAC ou `Authorization: Bearer`). Kuberpack n’a pas le kubeconfig du VPS ; le rollback Helm reste celui du HelmRelease.

Previews : URL `https://pr-<n>.<app>.<KUBERPACK_PREVIEW_DOMAIN>`.

CLI ou UI : plus tard (`docs/roadmap.md` phase 5). Pas de logs UI sur le control plane.


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

À l’enregistrement, Kuberpack matérialise ce JSON en YAML GitOps (`kubernetes/vps/apps/<name>/`). Ingress, pull secret, TLS et DNS viennent des défauts plateforme (`KUBERPACK_DNS_TARGET`, …), pas du dépôt app. Flux ne voit l’app qu’après le premier pin `image`.

## Suite (après le MVP HTTP)

`GET/DELETE /api/v1/apps`, liste des builds, URL de preview, `rolled_back`, redeploy, rollback manuel.

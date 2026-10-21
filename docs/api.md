# API

HTTP d’abord. CLI (`kuberpack`) ou UI ensuite. Pas d’exposition publique sans authentification.

## Déclarer une application

Une application se déclare par un **manifeste** : c’est la seule source de vérité.
Il n’y a plus d’API d’enregistrement : `POST /api/v1/projects` compile le
manifeste, arme le pipeline de build et écrit le GitOps.

```http
POST /api/v1/projects
```

```json
{
  "project": "demo",
  "manifest": "project: demo\nservices:\n  web:\n    repository: org/web\n    hostname: web.example.org\n"
}
```

Le format du manifeste est spécifié dans [manifest.md](manifest.md). Le
compilateur est strict : un champ inconnu ou une valeur non supportée est
une erreur explicite, jamais un no-op silencieux.

| Champ | Rôle |
| --- | --- |
| `project` | Nom du projet. Doit correspondre à `project:` du manifeste |
| `manifest` | Le `kuberpack.yaml` complet, en YAML |

Réponse : le projet compilé (`project`, `services`, `addons`, `infra_commit`,
`changed`).

## Ce que fait la compilation

Pour chaque service qui déclare un `repository`, Kuberpack :

1. écrit les artefacts GitOps du service (`values.yaml`, `helmrelease.yaml`,
   `kustomization.yaml`, `secret.yaml`) ;
2. crée l’entrée SQLite de l’app (`<projet>-<service>`), sa clé HMAC et son
   webhook Forgejo ;
3. enfile le premier build.

La compilation est idempotente : la recompiler ne duplique ni webhook, ni
secret, ni build. Un service qui quitte le manifeste perd son app, son webhook
et son secret.

Le `values.yaml` est régénéré depuis le manifeste à chaque compilation : le
manifeste est la source de vérité du runtime. Seuls l’`image` et le `track`
épinglés par le control plane sont conservés.

## Suite HTTP

`GET /api/v1/projects` — liste des projets.

`GET /api/v1/projects/{name}` — un projet et ses services.

`DELETE /api/v1/projects/{name}` — supprime le projet du GitOps, les apps, les
webhooks et les secrets associés.

`GET /api/v1/apps` — les apps, avec leur projet, leur chemin de valeurs et
leur webhook.

`GET /api/v1/apps/{name}` — une app.

`GET /api/v1/apps/{name}/builds` — historique SQLite (`status` : `running`,
`succeeded`, `failed`, `rolled_back`).

`POST /api/v1/apps/{name}/redeploy` — enfile un build du HEAD courant. Chaque
appel crée une livraison distincte : un redeploy du même commit relance bien
un build au lieu d’être ignoré comme doublon.

`POST /hooks/forgejo` — push (`push`, `pull_request`) signé en HMAC.

`POST /hooks/flux` — notification HelmRelease en erreur → `rolled_back` + statut
Forgejo `warning`. Secret `KUBERPACK_FLUX_WEBHOOK_SECRET` (HMAC ou
`Authorization: Bearer`). Kuberpack n’a pas le kubeconfig du VPS ; le rollback
Helm reste celui du HelmRelease.

Previews : URL `https://pr-<n>.<app>.<KUBERPACK_PREVIEW_DOMAIN>`.

CLI ou UI : plus tard (`docs/roadmap.md` phase 5). Pas de logs UI sur le control plane.


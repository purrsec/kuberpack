# API

Le CLI utilise l'API HTTP. Les endpoints de gestion exigent le jeton API ;
le développeur n'a pas besoin du kubeconfig du cluster.

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

`GET /api/v1/apps` — les apps, leurs métadonnées et leur objet `deployment`
observé dans le cluster au moment de la requête.

`GET /api/v1/apps/{name}` — une app.

`GET /api/v1/apps/{name}/status` — `name`, `project`, `hostname` et
`deployment` : état, raison, message, image du workload, réplicas
(`desired`, `ready`, `available`, `updated`), réconciliation et horodatage.
Un nom inconnu retourne 404 ; l'observation nécessite la même authentification
que les autres endpoints de gestion.

Cet endpoint ajoute `deployment.resources` : usage CPU/RAM, demandes et
limites, pourcentage de la limite, redémarrages, détail des conteneurs et
compteurs de bridage CPU. Les endpoints d'inventaire conservent une observation
de disponibilité légère ; les ressources sont lues dans `app status`.

`GET /api/v1/apps/{name}/builds` — historique SQLite (`status` : `running`,
`succeeded`, `failed`, `rolled_back`).

`GET /api/v1/apps/{name}/logs` — logs des conteneurs de l'application,
en texte et en streaming. Options : `follow`, `tailLines` (100 par défaut,
`-1` pour tout), `sinceSeconds`, `previous`, `timestamps`, `container`
(`app` par défaut). L'endpoint exige le jeton API, également pour une image
publiée. Un nom inconnu retourne 404, une option invalide 400 et des logs
indisponibles 503. Une interruption après le début du flux est signalée
par le trailer HTTP `X-Kuberpack-Log-Error` ; le CLI retourne alors une erreur.

`GET /api/v1/apps/{name}/builds/{id}/logs` — sortie du builder, conservée
avec le build ou lue dans son pod pendant son exécution.

`POST /api/v1/apps/{name}/redeploy` — enfile un build du HEAD courant. Chaque
appel crée une livraison distincte : un redeploy du même commit relance bien
un build au lieu d’être ignoré comme doublon.

`POST /hooks/forgejo` — push (`push`, `pull_request`) signé en HMAC.

`POST /hooks/flux` — notification HelmRelease en erreur → `rolled_back` + statut
Forgejo `warning`. Secret `KUBERPACK_FLUX_WEBHOOK_SECRET` (HMAC ou
`Authorization: Bearer`). Kuberpack n’a pas le kubeconfig du VPS ; le rollback
Helm reste celui du HelmRelease.

Previews : URL `https://pr-<n>.<app>.<KUBERPACK_PREVIEW_DOMAIN>`.

## Consulter les déploiements

```bash
kuberpack apps
kuberpack app status stirling-pdf-pdf
kuberpack apps --json
kuberpack app status --json stirling-pdf-pdf
```

`apps` affiche `NAME`, `PROJECT`, `STATUS`, `READY` et `HOSTNAME`.
`app status` donne les réplicas mis à jour et disponibles, l'image observée,
la réconciliation et la raison détaillée d'un blocage. `--json` conserve la
réponse complète pour les scripts. Consulter un état en erreur ne fait pas
échouer la commande ; les erreurs HTTP ou d'authentification l'échouent.

Le détail affiche aussi `CPU`, `MEMORY`, `RESTARTS`, `THROTTLING`, `METRICS`
et les horodatages/fenêtres des échantillons. L'usage est rapporté aux limites
réelles des conteneurs des pods observés, avec les demandes en complément.
L'état de disponibilité du déploiement et la disponibilité des mesures sont
indépendants : une absence de métriques ne transforme pas un déploiement sain
en déploiement en erreur.

| État | Signification |
| --- | --- |
| `running` | La génération courante est observée ; tous les réplicas demandés sont mis à jour, prêts et disponibles. |
| `deploying` | Une génération, une réconciliation ou des réplicas sont encore en cours de convergence. |
| `pending` | Le workload n'est pas encore créé, ou ses pods attendent des ressources de scheduling. |
| `degraded` | Une erreur de rollout ou de réconciliation est constatée alors que des réplicas servent encore. |
| `failed` | Le rollout ou le démarrage échoue sans réplicas disponibles. |
| `stopped` | Le manifeste demande zéro réplica et aucun réplica ne subsiste. |
| `unknown` | Le control plane ne peut pas lire les observations nécessaires. Les réplicas inconnus s'affichent `-`. |

Les conditions d'une ancienne génération ne valident pas le nouveau rollout.
Les erreurs de pods proviennent uniquement de la ReplicaSet contrôlée par le
Deployment et portant sa dernière révision ; les pods d'un ancien rollout
ou en suppression ne décrivent pas le nouveau déploiement. Une réconciliation
suspendue est indiquée séparément ; elle n'implique pas l'arrêt de l'application.
Ces observations s'appuient sur les statuts
[Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#deployment-status)
et [HelmRelease](https://fluxcd.io/flux/components/helm/helmreleases/#helmrelease-status).

Le compilateur génère les droits de lecture dans le namespace du projet,
également pour les services utilisant une image publiée et avant le premier
build. Ils ne permettent ni de modifier les workloads ni de lire les Secrets.
L'inventaire conserve le namespace réel, y compris lorsqu'il diffère du projet.
La lecture des applications utilise au plus quatre observations en parallèle,
avec un budget global de huit secondes ; une observation indisponible
n'empêche pas de retourner l'inventaire.

### Mesures de ressources et bridage CPU

`resources.cpu` et `resources.memory` utilisent les champs `usage`, `request`,
`limit`, `unit` et `percent_of_limit`. Le CPU est exprimé en millicœurs
(`unit: millicores`, 1000m = un cœur), la mémoire en octets (`unit: bytes`),
affichés en Mio dans le CLI. Le CPU est une moyenne sur la fenêtre renvoyée
par l'[API de ressources Kubernetes](https://kubernetes.io/docs/tasks/debug/debug-cluster/resource-metrics-pipeline/) ;
la mémoire correspond au working set exposé par cette API.
Le CPU est arrondi au millicœur supérieur, comme `kubectl top` ; les
pourcentages utilisent cette valeur et ne sont pas plafonnés à 100 %.

La source est `metrics.k8s.io/v1beta1`, généralement fournie par metrics-server.
Le compilateur génère le droit `list pods` de ce groupe uniquement dans le
namespace du projet. Les pods sont filtrés par leurs propriétaires, comme
pour les logs. Les conteneurs ordinaires et les init containers persistants
(`restartPolicy: Always`) sont comptés ; les conteneurs init terminés et les
pods en suppression sont exclus. Les demandes et limites sont celles des
pods actuels, y compris pendant un rollout.

`resources.state` vaut `available`, `partial`, `unavailable` ou `stopped`.
Une valeur absente reste absente, sans zéro inventé. Si un conteneur manque,
les usages agrégés et leurs pourcentages restent indisponibles ; les mesures
individuelles valides restent présentes dans `resources.containers`.
Les échantillons vieux de plus de deux minutes, datés de plus de trente
secondes dans le futur, antérieurs au pod ou au démarrage actuel du
conteneur, et les valeurs invalides sont écartés. Chaque échantillon expose
`sampled_at` et `window_seconds`. Une limite absente signifie qu'aucune
limite complète n'est déclarée pour ce périmètre, sans pourcentage calculé.

Le bridage utilise directement `/metrics/cadvisor` du kubelet sur HTTPS
vérifié, sans commande exécutée dans les conteneurs. Les familles sont
`container_cpu_cfs_periods_total`, `container_cpu_cfs_throttled_periods_total`
et `container_cpu_cfs_throttled_seconds_total`. Les compteurs sont corrélés
au namespace, au pod, au conteneur et à l'identifiant runtime courant ; les
anciennes instances après un redémarrage sont écartées. Le champ
`scope: current_container_lifetime` indique un **cumul depuis le démarrage
des conteneurs actuels**. `percent_of_periods` mesure la proportion de périodes
CPU ayant subi un bridage, pondérée par les périodes de chaque conteneur.
Il ne s'agit ni d'une utilisation CPU actuelle, ni d'un taux de temps perdu.

`throttling.state` vaut `observed`, `not_observed`, `not_limited`, `partial`
ou `unavailable`. Une famille absente, un refus d'accès, un endpoint inaccessible
ou une autorité de certification différente donnent une indisponibilité
explicite. Les totaux ne sont calculés que si tous les conteneurs avec une
limite CPU sont observés. Ces données ne constituent pas un historique après
suppression des pods.

Le chart du control plane ajoute `get nodes` et `get nodes/metrics`, droits
de lecture cluster nécessaires pour joindre les kubelets. Il n'accorde
aucun `nodes/proxy`, accès aux Secrets ou `exec`. La séparation des droits
kubelet est définie par [Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/kubelet-authn-authz/#kubelet-authorization).
`nodeMetrics.enabled: false` retire ces droits du chart ; les mesures CPU/RAM
restent indépendantes. `nodeMetrics.caBundle` peut fournir une CA PEM dédiée
aux kubelets ; hors chart, `KUBERPACK_KUBELET_CA_PEM` fournit la même option.
Par défaut, la CA de l'API Kubernetes est utilisée. La vérification TLS n'est
jamais désactivée pour ce diagnostic. Les kubelets doivent être accessibles
depuis le control plane ; les distributions managées peuvent restreindre
cet accès ou ne pas exposer les compteurs CFS.

Le budget de lecture des ressources est de quatre secondes, dans le budget
global de huit secondes de la requête. Les scrapes kubelet sont limités à
deux en parallèle et huit Mio par réponse ; seuls les compteurs de quota
sont décodés. Aucun nouveau service de monitoring n'est installé.

`app builds` et `app build-logs` donnent l'historique et la sortie du **builder**.
Le statut décrit les workloads actuellement observés ; il ne prouve pas
qu'un commit encore en cours de build est déjà déployé. Après compilation,
l'observation peut encore refléter l'ancienne configuration jusqu'à
l'application du commit par Flux.

## Consulter les logs

```bash
kuberpack app logs stirling-pdf-pdf
kuberpack app logs stirling-pdf-pdf -f
kuberpack app logs stirling-pdf-pdf --tail 50 --since 10m --timestamps
kuberpack app logs stirling-pdf-pdf --previous
kuberpack app build-logs demo-web
kuberpack app build-logs --build 7 demo-web
```

`app logs` lit les logs d'exécution des services `image:` comme de ceux
construits depuis un dépôt. Une image publiée telle que Stirling n'a pas de
build Kuberpack, mais ses conteneurs produisent des logs. Les anciennes
commandes `app logs --build 7 demo-web` restent valides ; `--build latest`
lit le dernier build. Ces options ne se combinent pas avec celles du runtime.

La lecture utilise client-go et l'API Kubernetes, avec la sémantique de
[`kubectl logs`](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_logs/).
Le développeur a uniquement besoin de l'URL et du jeton API Kuberpack.
`--tail` s'applique à chaque réplica ; `--since` accepte une durée positive
(`30s`, `10m`, `2h`), `--timestamps` ajoute les horodatages Kubernetes.
`--previous` lit l'instance précédente du conteneur dans les pods existants,
utile après un crash ; il ne récupère pas les pods supprimés. `-c`/`--container`
permet de nommer un autre conteneur, y compris un init container.

Les pods sont résolus par leur chaîne de propriétaires Deployment →
ReplicaSet → Pod, dans le namespace réel du service. Les hooks de test Helm,
les pods étrangers et les pods en suppression sont exclus ; les conteneurs
qui n'ont jamais démarré sont ignorés. Les logs des réplicas lisibles sont
fusionnés, avec un préfixe `[pod/conteneur]` lorsqu'il y en a plusieurs.
La lecture est limitée à cinq réplicas simultanés, avec une erreur explicite
au-delà. `-f`/`--follow` suit les pods trouvés au lancement jusqu'à leur fin
ou Ctrl-C ; relancer la commande après leur remplacement par un rollout.
Ces logs ne sont pas archivés par Kuberpack.

Le compilateur ajoute seulement `get` sur `pods/log` aux droits du namespace
du projet. Aucun accès aux Secrets, à `exec` ou à la modification des workloads
n'est ajouté. L'API vérifie l'appartenance des pods avant d'ouvrir les flux.


# API HTTP

Le client développeur utilise l'API v2 pour l'identité, les projets, les plans et les demandes de déploiement. Toutes les routes métier exigent `Authorization: Bearer …`. Les routes de diagnostic v1 restent disponibles ; leurs anciennes écritures ne permettent plus de contourner le nouveau contrat.

## Identité et permissions

`GET /api/v2/session` retourne la version, les capacités annoncées, l'identité du credential, le rôle, le projet, les restrictions de services et l'expiration. Le secret n'y figure jamais. Le client exige les capacités de plan, déploiement, idempotence, rôles et observation de configuration avant toute publication.

Les nouveaux rôles sont `reader`, `deployer`, `maintainer` et `admin`. Les anciens jetons `project` conservent la lecture mais ne gagnent aucun pouvoir de publication. Un jeton restreint à un service ne peut pas lire le manifeste complet ni les déploiements concernant d'autres services.

## Routes v2

| Méthode et chemin | Contrat |
| --- | --- |
| `GET /api/v2/session` | Credential, version et capacités. |
| `GET /api/v2/projects` | Projets accessibles, y compris les réservations vides. |
| `GET /api/v2/projects/{project}` | Métadonnées, révision, services et manifeste complet si autorisé. |
| `GET /api/v2/projects/{project}/services` | Identités stables et observations des services autorisés. |
| `POST /api/v2/projects/{project}/plans` | Préparer `deploy`, `redeploy` ou, pour admin, `destroy`. |
| `POST /api/v2/projects/{project}/deployments` | Accepter un plan avec une clé d'idempotence ; réponse `202` et ID durable. |
| `GET /api/v2/projects/{project}/deployments` | Historique des demandes visibles. |
| `GET /api/v2/projects/{project}/deployments/{id}` | État, étapes, builds liés, révisions et erreur éventuelle. |
| `GET /api/v2/projects/{project}/requests/{id}` | Retrouver une demande de ce credential par sa clé d'idempotence. |
| `POST /api/v2/admin/projects/{project}` | Réserver un projet vide ; admin uniquement. |
| `PUT /api/v2/admin/projects/{project}/policy` | Politique opérateur, quotas et identité de secrets. |
| `POST /api/v2/admin/tokens` | Rôle et expiration obligatoires ; secret rendu une fois. |
| `GET /api/v2/admin/tokens` | Métadonnées sans secret. |
| `DELETE /api/v2/admin/tokens/{id}` | Révocation idempotente. |

## Plans et exécution

Un plan appartient au credential et au projet, expire après quinze minutes et contient la révision de base, l'empreinte de politique, les changements et les références immuables. Sa préparation compile dans un checkout temporaire et ne pousse aucun commit, ne pose aucun webhook et n'enfile aucun build.

```json
{"action":"deploy","manifest":"project: demo\nservices: {}\n","expected_revision":"empreinte-du-manifeste-lu"}
```

`expected_revision: ""` exige un projet encore vide. Le manifeste est complet. `redeploy` utilise `service` et éventuellement `commit` ; la première version exige un SHA égal au HEAD vérifié de la branche. Les permissions et politiques sont vérifiées avant préparation puis avant exécution.

```json
{"plan_id":"plan_ID","request_id":"pipeline-42","fingerprint":"empreinte-des-entrees-client"}
```

Un identifiant réutilisé avec le même contenu retourne la même demande. Un contenu différent donne `409`. Une seule demande est active par projet. Le travail est persisté avant les effets distants. Git, SQLite, la forge et Kubernetes ne forment pas une transaction : une erreur après commit GitOps est présentée comme une erreur partielle avec l'étape effectuée.

Le suivi ne valide pas une ancienne application saine : il corrèle les builds de la demande, le digest attendu, la révision de configuration annotée sur le workload et sa génération observée. Les états terminaux sont `succeeded`, `failed`, `rolled_back` et `superseded`. Un build interrompu par un redémarrage est conservé en échec et n'est pas rejoué aveuglément.

Les erreurs v2 sont des objets `error` contenant `code` et `message`. `401` indique une authentification refusée, `403` un droit ou une politique refusés et `409` un conflit de révision, de plan ou d'idempotence. Les limites générales répondent `429` avec `Retry-After`.

## Routes historiques

`POST /api/v1/projects`, `DELETE /api/v1/projects/{name}` et `POST /api/v1/apps/{name}/redeploy` sont retirées : `410` ou refus de permission. L'administration des jetons via v1 applique également les règles explicites de rôle et d'expiration. Les lectures d'applications, de status, de builds et de logs gardent leur périmètre projet/service. Les apps d'un autre projet sont masquées par `404`.

`GET /healthz` reste une sonde publique minimale. Les webhooks `/hooks/git`, `/hooks/forgejo` et `/hooks/flux` restent des intégrations machine signées avec leurs secrets propres ; ils ne sont pas des commandes du client développeur.

## Consulter les déploiements

```bash
kuberpack services list
kuberpack status --service stirling-pdf-pdf
kuberpack services list --output json
kuberpack status --service stirling-pdf-pdf --output json
```

`services list` affiche `NAME`, `PROJECT`, `STATUS`, `READY` et `HOSTNAME`.
`status --service` donne les réplicas mis à jour et disponibles, l'image observée,
la réconciliation et la raison détaillée d'un blocage. `--output json` conserve la
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

`builds list` et `builds logs` donnent l'historique et la sortie du **builder**.
Le statut décrit les workloads actuellement observés ; il ne prouve pas
qu'un commit encore en cours de build est déjà déployé. Après compilation,
l'observation peut encore refléter l'ancienne configuration jusqu'à
l'application du commit par Flux.

## Consulter les logs

```bash
kuberpack logs --service stirling-pdf-pdf
kuberpack logs --service stirling-pdf-pdf -f
kuberpack logs --service stirling-pdf-pdf --tail 50 --since 10m --timestamps
kuberpack logs --service stirling-pdf-pdf --previous
kuberpack builds logs --latest --service demo-web
kuberpack builds logs 7 --service demo-web
```

`logs` lit les logs d'exécution des services `image:` comme de ceux
construits depuis un dépôt. Une image publiée telle que Stirling n'a pas de
build Kuberpack, mais ses conteneurs produisent des logs. La lecture utilise client-go et l'API Kubernetes, avec la sémantique de
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


# À faire

Chantiers ouverts, du plus utile pour le développeur au plus structurel. Ils ne
sont pas écrits dans le code : ce document fixe le contrat visé, l’endroit où
le travail tombe, et ce qui vaut dire « fait ».

| # | Chantier | Statut |
| --- | --- | --- |
| 1 | [Logs de build](#1-logs-de-build) | fait |
| 2 | [Dimensionnement CPU/RAM](#2-dimensionnement-cpuram) | fait |
| 3 | [CLI de pilotage](#3-cli-de-pilotage) | fait |
| 4 | [Le manifeste pilote le déploiement](#4-le-manifeste-pilote-le-déploiement) | fait |
| 5 | [Les addons](#5-les-addons) | fait |
| 6 | [Tests et gate de release](#6-tests-et-gate-de-release) | en qualification |

La phase 5 de la [feuille de route](docs/roadmap.md) couvre le point 3.

---

## 1. Logs de build

### Contexte

Aujourd’hui, quand un build échoue, l’API répond `error: "job: failed"`. La
cause réelle est dans les logs du pod : elle n’est ni dans SQLite, ni dans
l’API, ni dans le dépôt applicatif. C’est le premier reproche fait sur
l’outil : « ça a échoué, mais pourquoi ? ».

Le mécanisme existe à moitié. Le builder écrit déjà son rapport (log + SBOM)
dans le dépôt d’archive quand `KUBERPACK_BUILDS_GIT_URL` est défini
(`internal/buildsarchive`), et le Job garde ses logs une heure
(`TTLSecondsAfterFinished`). Mais l’archive est optionnelle, hors du chemin
produit, et les logs du pod disparaissent avec le TTL.

### Contrat

```http
GET /api/v1/apps/{name}/builds/{id}/logs
```

Réponse : les logs du build (extrait de la sortie du conteneur `builder`),
en texte. Le client peut suivre le build en cours : la ressource renvoie ce
qui est disponible tant que le Job tourne, puis se referme.

```http
GET /api/v1/apps/{name}/builds/{id}
```

Ajoute la raison d’échec lisible, extraite de la sortie du builder et non
seulement du statut du conteneur. Aujourd’hui : `"job: failed"`, demain :
`"trivy: 2 CRITICAL (CVE-2025-…)"` ou `"scan: image target not scanned"`.

### Où

| Élément | Emplacement |
| --- | --- |
| Endpoint | `internal/control/apps.go` (route `GET /api/v1/apps/{name}/builds/{id}` et `…/logs`) |
| Lecture des logs du pod | `internal/builder` (le `JobRunner` a déjà le `kubernetes.Interface`) |
| Raison d’échec | `internal/fail` (les étapes existent : `Job`, `Trivy`, `Scan`, `SBOM`…) |
| Persistance | `internal/store` (colonne `error` déjà là, à remplir avec la vraie cause) |
| Archive | `internal/buildsarchive` (l’extrait existe, à rendre systématique) |

Le control plane ne parle qu’au cluster où il tourne ; le builder y est
aussi. Lire les logs d’un Job n’est donc pas un accès hors cluster : c’est
un droit RBAC à ajouter au chart (`pods/log`).

### Terminé quand

- l’API renvoie la cause réelle d’un échec de build ;
- `GET …/builds/{id}/logs` renvoie la sortie du builder, en cours ou après ;
- l’extrait de log et la SBOM sont archivés par défaut, sans configuration
  supplémentaire.

### Étape 6 — les logs de build (fait)

La cause d’un échec est lisible, et la sortie du builder est gardée.

| Élément | Emplacement |
| --- | --- |
| Sortie du build | `internal/control/buildlogs.go` (log du pod `builder`, extrait borné à 200 Ko) |
| Persistance | `internal/store` (colonne `builds.logs`, migration `ALTER TABLE builds ADD COLUMN logs`) |
| API | `GET /api/v1/apps/{name}/builds/{id}/logs`, `…/builds/{id}` (raison lisible) |
| CLI | `kuberpack app logs <app>` (en direct ou stocké), `kuberpack app builds <app>` |

Un build qui échoue dit pourquoi : `disk: le noeud a 1.4 GiB libres, un
build en demande 2.0 GiB`, `trivy: CVE-2026-59873 in tar`, `push: …`. Le
log complet est dans l’archive du dépôt des builds quand il est configuré.

**Pièges évités** :

- **Le label de la livraison est trop long.** Un redemarrage a un nom de
  livraison de plus de 63 caractères, et Kubernetes refuse un label plus
  long : le build échouait avant de commencer. Le label porte une empreinte
  (`internal/builder.DeliveryLabel`), et la plateforme cherche le pod du
  build de la même façon.
- **Le nom du Job est stable par livraison.** Un redemarrage du même commit
  retrouve le Job précédent ; le Job qui a échoué est supprimé puis recréé.
- **Le disque du noeud est une dimension.** Un build écrit l’image de
  l’application, ses couches et la base de Trivy. Le builder mesure le
  disque et refuse le build **avant** qu’il ne démarre, en disant l’espace
  qui manque (`internal/buildexec/disk.go`).

### Étape 5 — le manifeste est recompilé par un push (fait)

Le manifeste vivait dans le dépôt GitOps, mais seul `POST /api/v1/projects`
le compilait : éditer le manifeste dans Git ne changeait rien.

Kuberpack pose maintenant un webhook sur le dépôt GitOps. Un push qui touche
un `kuberpack.yaml` recompile le projet : le manifeste est **lu** depuis le
dépôt et **jamais réécrit** (les commentaires du développeur survivent). Un
push qui supprime un manifeste supprime le projet, l’app, son webhook et son
secret.

Le statut de commit sur le push dit audeveloppeur si son manifeste compile :
`1 project(s) compiled`, ou l’erreur.

**Pièges evités** : le fichier `values.yaml` est canonisé (compilateur et
control plane écrivent la même forme), sinon chaque build déclencherait un
commit parasite ; le compilateur applique les mêmes défauts que l’ancien
chemin (port 8080, healthcheck `/`).

### Pièges

- **Taille.** Un build qui échoue peut produire des mégabytes de logs. On
  garde un extrait borné (derniers ~200 Ko) en base, l’archive garde tout.
- **Un build par Job.** Le nom du Job est stable par livraison
  (`kuberpack-build-<hex>`) : c’est ce nom qu’il faut résoudre pour lire les
  logs, pas le nom de l’app.
- **Distinguer `builder` et `buildkitd`.** Le sidecar BuildKit a ses propres
  logs (progression, snapshots). Les deux sont utiles, mais un seul par
  défaut.

---

## 2. Dimensionnement CPU/RAM

### Contexte

Une application enregistrée n’a aucune notion de taille : les `resources` du
chart `stateless` s’appliquent à tout le monde (50m/64Mi en requêtes,
500m/256Mi en limites), et `replicas` est forcé à 1 (`internal/promote/app.go`,
`normalizeSpec`). Le développeur ne peut pas dire « cette app a besoin de
plus », ni « cette app doit tourner en 3 exemplaires ».

C’est cohérent avec l’homelab, pas avec un PaaS. Sur un cluster partagé, la
taille d’une app est la première question.

### Contrat

À l’enregistrement et au `PATCH` :

```json
{
  "resources": {
    "requests": { "cpu": "100m", "memory": "256Mi" },
    "limits":   { "cpu": "1",    "memory": "1Gi" }
  },
  "replicas": 2
}
```

Le format est celui de Kubernetes, sans traduction : ce que le developer écrit
est ce qui atterrit dans le `values.yaml`. `replicas` reste à part, il
conditionne le Service, pas seulement le Deployment.

### Où

| Élément | Emplacement |
| --- | --- |
| Requête HTTP | `internal/control/apps.go` (`createAppBody`, `patchApp`) |
| Spécification | `internal/promote/app.go` (`AppSpec.Resources`, `AppSpec.Replicas`) |
| Rendu GitOps | `internal/promote/app.go` (`valuesYAML` : n’écrit `resources` que pour les previews aujourd’hui) |
| Patch runtime | `internal/promote/values.go` (`PatchRuntime` : réécrit hostname, port, healthcheck, networkPolicy — pas les ressources) |
| Chart | `charts/stateless/values.yaml` (les valeurs par défaut restent) |
| Contrôle | `charts/kuberpack/templates/quota.yaml` (le quota du namespace `apps` borne la somme) |

### Terminé quand

- `resources` et `replicas` sont acceptés à l’enregistrement et au `PATCH`,
  et rendus dans le `values.yaml` ;
- un `PATCH` des ressources réécrit le GitOps **sans** toucher l’`image` ni
  le `track` ;
- la valeur par défaut reste celle du chart : une app sans `resources`
  déclarées se comporte comme aujourd’hui.

### Étape 6 — la taille suit le noeud (fait)

L’enregistrement des apps a disparu avec le manifeste (étape 3 du point 4) :
la taille se déclare dans `kuberpack.yaml`, et elle est rendue telle quelle
dans le `values.yaml` (`resources`, `replicas`).

Les dimensions du **noeud** sont bornées par la plateforme :

| Dimension | Règle | Emplacement |
| --- | --- | --- |
| Mémoire du builder | un quart du noeud chacun, builder et BuildKit | `internal/builder/job.go` (`memoryLimits`) |
| Mémoire déclarée | `KUBERPACK_NODE_MEMORY` : le compilateur refuse un projet qui ne tient pas | `internal/promote/capacity.go` |
| Cache de BuildKit | borné : BuildKit supprime les entrées les plus anciennes | `--oci-worker-gc-keepstorage` |
| Disque du noeud | un build qui ne tient pas est refusé, avec l’espace qui manque | `internal/buildexec/disk.go` |
| Volume de build | le travail, le cache et le Scratch sont bornés | `internal/builder/job.go` (`volumeSizes`) |

Le seuil de disque est configurable (`KUBERPACK_MIN_FREE_DISK`, `minFreeDisk`
du chart) : un petit noeud l’accepte plus bas qu’un grand.

**Les pods de l'application portent les etiquettes de la plateforme.** La
NetworkPolicy d'un addon selectionne les applications qui peuvent
l'atteindre par leurs etiquettes. Un pod sans elles est refuse par sa
propre base de donnees : l'erreur est « connection refused », et elle ne
dit pas pourquoi.

**Une application ne doit pas affamer la plateforme.** Un JVM lancé sur un
noeud de trois coeurs a pris les coeurs du control plane : l’API a
repondu « timeout » pendant que l’application demarrait. Le manifeste peut
dire `replicas: 0` (l’application est declaree, elle ne tourne pas) et
`startup_budget` (le temps de demarrage). Ce qui manque : la plateforme
refuse un projet dont les limites CPU et RAM dépassent la part du noeud
que le control plane se reserve, au lieu de laisser l’application affamer
la plateforme qui doit la deployer.

### Étape 5 — le manifeste est recompilé par un push (fait)

Le manifeste vivait dans le dépôt GitOps, mais seul `POST /api/v1/projects`
le compilait : éditer le manifeste dans Git ne changeait rien.

Kuberpack pose maintenant un webhook sur le dépôt GitOps. Un push qui touche
un `kuberpack.yaml` recompile le projet : le manifeste est **lu** depuis le
dépôt et **jamais réécrit** (les commentaires du développeur survivent). Un
push qui supprime un manifeste supprime le projet, l’app, son webhook et son
secret.

Le statut de commit sur le push dit audeveloppeur si son manifeste compile :
`1 project(s) compiled`, ou l’erreur.

**Pièges evités** : le fichier `values.yaml` est canonisé (compilateur et
control plane écrivent la même forme), sinon chaque build déclencherait un
commit parasite ; le compilateur applique les mêmes défauts que l’ancien
chemin (port 8080, healthcheck `/`).

### Pièges

- **La NetworkPolicy et la PSA `restricted` ne bougent pas.** Changer la
  taille ne change ni les probes, ni le `runAsUser`.
- **Un `PATCH` de ressources est un déploiement.** Il écrit dans Git, donc il
  déclenche une réconciliation Flux : c’est voulu, mais ça doit rester
  visible côté API (la réponse dit si le GitOps a changé).
- **Le quota du namespace.** Si le total des `limits` dépasse le quota
  `apps`, le Deployment est refusé par le scheduler. L’erreur doit
  remonter telle quelle, pas comme un échec de Helm opaque.

---

## 3. CLI de pilotage

### Contexte

Le CLI actuel (`cmd/kuberpack`) est un jeu de commandes d’opérateur :
`promote`, `trigger`, `serve`, `project`, `builder`. Rien pour répondre à
« où en est mon déploiement ? ».

L’API sait déjà presque tout : apps, builds, digest, commit d’infra, track.
Il manque le vocabulaire côté développeur et un client qui consume l’API
comme `project` le fait déjà (`KUBERPACK_API_URL`, `KUBERPACK_API_TOKEN`).

### Commandes visées

```bash
kuberpack apps                       # liste des applications et leur état
kuberpack app <name>                 # détail : hostname, port, track, digest, dernier build
kuberpack builds <name>              # historique des builds
kuberpack logs <name> [--build id]  # logs du build (point 1)
kuberpack redeploy <name>            # relance le build du HEAD
```

`kuberpack apps` et `kuberpack app <name>` sont le point d’entrée : c’est la
question que le developer pose en premier, et celle à laquelle la réponse
doit tenir en un écran.

### Où

| Élément | Emplacement |
| --- | --- |
| Commandes | `cmd/kuberpack/` (un fichier par commande, comme `serve.go`) |
| Client HTTP | nouveau, ou extension de `internal/forgejo` (même style d’auth `Bearer`) |
| Rendu | `internal/control` (l’API expose déjà l’état nécessaire) |

Le CLI reste un **client de l’API** : il ne parle ni à Kubernetes, ni à
Forgejo en direct pour l’état d’une app. C’est ce qui lui permet de
fonctionner depuis un poste, sans kubeconfig.

### Terminé quand

- `apps`, `app`, `builds` et `redeploy` fonctionnent contre l’API ;
- la sortie est lisible sans `jq` (tableau par défaut, `--json` disponible) ;
- les codes de sortie sont exploitables dans un script.

### Étape 6 — le CLI de pilotage (fait)

| Commande | Ce qu’elle fait |
| --- | --- |
| `kuberpack apps` | les applications, leur projet, leur source, leur hote |
| `kuberpack app builds <app>` | l’historique des builds, avec la raison de l’échec |
| `kuberpack app logs <app>` | la sortie du builder : en direct pendant le build, stockée après |
| `kuberpack app redeploy <app>` | relance le build du commit courant |
| `kuberpack project apply -f kuberpack.yaml` | compile et applique un manifeste |
| `kuberpack project create --image …` | crée un projet depuis une image publique |

Le client parle à l’API (`KUBERPACK_API_URL`, `KUBERPACK_API_TOKEN`) et
n jamais à Kubernetes : il marche depuis un poste, sans kubeconfig.

### Étape 5 — le manifeste est recompilé par un push (fait)

Le manifeste vivait dans le dépôt GitOps, mais seul `POST /api/v1/projects`
le compilait : éditer le manifeste dans Git ne changeait rien.

Kuberpack pose maintenant un webhook sur le dépôt GitOps. Un push qui touche
un `kuberpack.yaml` recompile le projet : le manifeste est **lu** depuis le
dépôt et **jamais réécrit** (les commentaires du développeur survivent). Un
push qui supprime un manifeste supprime le projet, l’app, son webhook et son
secret.

Le statut de commit sur le push dit audeveloppeur si son manifeste compile :
`1 project(s) compiled`, ou l’erreur.

**Pièges evités** : le fichier `values.yaml` est canonisé (compilateur et
control plane écrivent la même forme), sinon chaque build déclencherait un
commit parasite ; le compilateur applique les mêmes défauts que l’ancien
chemin (port 8080, healthcheck `/`).

### Pièges

- **Le CLI ne doit pas devenir un second client Kubernetes.** Toute
  information affichée doit venir de l’API, sinon les deux clients divergent.
- **Les builds peuvent être vieux.** L’historique est en SQLite : un build
  inconnu ou purgé doit le dire, pas renvoyer une ligne vide.
- **`redeploy` est asynchrone.** La commande doit le dire et proposer de
  suivre le build (`--watch`), sinon elle donne l’impression d’avoir
  échoué.

---

## 4. Le manifeste pilote le déploiement

`kuberpack.yaml` devient la source de vérité unique : il décrit ce qui est
déployé, le control plane en déduit les apps et leurs builds. Le format est
spécifié dans [docs/manifest.md](docs/manifest.md).

Les breaking changes sont autorisés : il n’y a aucun utilisateur.

### Étape 1 — le manifeste est honnête (fait)

Un manifeste ne peut plus déclarer un champ que le compilateur ignore : le
parsing est **strict** (un champ inconnu est une erreur) et le compilateur
refuse explicitement ce qu’il ne sait pas faire (`type: worker`, un addon
autre que `cnpg`, `backup.s3`, un `peer` inconnu, une stratégie invalide).

Les champs qui ne servaient à rien servent maintenant :

| Champ | Effet |
| --- | --- |
| `services.*.replicas` | rendu dans le `values.yaml` (défaut 1, jamais 0) |
| `services.*.env` | rendu en liste `env` triée (le compilateur est déterministe) |
| `services.*.build.*` | acceptés, validés (alimentent l’app record, étape 2) |
| `services.*.branch`, `strategy` | acceptés, validés (idem) |
| `policies.defaultDeny` | pilote la `NetworkPolicy` du chart |
| `addons.*.backup.retention` | rendu en `retentionPolicy` CNPG |
| `namespace` | le namespace du projet, réel : `HelmRelease`, `Cluster` CNPG, Secret applicatif et objet `Namespace` |

### Étape 2 — le manifeste arme le pipeline (fait)

C’est le cœur : avant, `services.*.repository` était **inerte** — le
manifeste compilait du GitOps, mais le build ne partait que si l’app avait
aussi été enregistrée via `POST /api/v1/apps`.

Le compilateur produit maintenant, en plus des fichiers GitOps, la
**description des apps** (repository, branche, stratégie, overrides de build,
chemin des valeurs). Le control plane les réconcilie dans SQLite et pose les
webhooks Forgejo.

| Élément | Emplacement |
| --- | --- |
| Sortie du compilateur | `internal/promote/project.go` (`ProjectResult.Apps`) |
| Réconciliation | `internal/control/reconcile.go` (store + client Forgejo) |
| App record | `internal/store` (`project`, `values_path` ; l’app est une projection du manifeste) |
| Webhook | `internal/forgejo` (`EnsureHook`, idempotent) |

Le nom d’une app est global : `<projet>-<service>`. Deux projets peuvent
déclarer un service du même nom sans se marcher dessus.

### Étape 3 — l’API d’enregistrement disparaît (fait)

`POST/PATCH/DELETE /api/v1/apps` faisaient doublon avec le manifeste : deux
écrivains pour la même chose, donc deux sources de vérité. Ils sont
**supprimés** (breaking change assumé) : le manifeste est la seule déclaration
possible.

Le code correspondant (`EnsureApp`, `SyncRuntime`, `DisableApp`) a été
supprimé avec eux : il ne reste qu’un chemin d’écriture.

`GET /api/v1/apps` et `GET /api/v1/apps/{name}/builds` restent : c’est de l’état
opérationnel. `POST /api/v1/apps/{name}/redeploy` reste : c’est une action, pas
une déclaration.

### Étape 4 — le chemin Flux des projets (fait)

Le `Kustomization` Flux watche `./kubernetes/vps/apps`, alors que les projets
vivent dans `./kubernetes/vps/projects` : **les projets compilés n’étaient
jamais réconciliés**. Il faut un second `Kustomization` Flux, un par arbre.

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-homelab-projects
  namespace: flux-system
spec:
  interval: 1m
  path: ./kubernetes/vps/projects
  prune: true
  sourceRef: {kind: GitRepository, name: infra-homelab}
```

C’est le seul réglage côté opérateur : sans lui, un manifeste compilé reste
inerte.

### Étape 5 — le manifeste est recompilé par un push (fait)

Le manifeste vivait dans le dépôt GitOps, mais seul `POST /api/v1/projects`
le compilait : éditer le manifeste dans Git ne changeait rien.

Kuberpack pose maintenant un webhook sur le dépôt GitOps. Un push qui touche
un `kuberpack.yaml` recompile le projet : le manifeste est **lu** depuis le
dépôt et **jamais réécrit** (les commentaires du développeur survivent). Un
push qui supprime un manifeste supprime le projet, l’app, son webhook et son
secret.

Le statut de commit sur le push dit audeveloppeur si son manifeste compile :
`1 project(s) compiled`, ou l’erreur.

**Pièges evités** : le fichier `values.yaml` est canonisé (compilateur et
control plane écrivent la même forme), sinon chaque build déclencherait un
commit parasite ; le compilateur applique les mêmes défauts que l’ancien
chemin (port 8080, healthcheck `/`).

### Pièges

- **La compilation doit rester idempotente.** Un projet recompilé ne doit
  jamais toucher un `values.yaml` déjà épinglé (règle d’or n°2). Le
  `values.yaml` est régénéré depuis le manifeste, mais l’`image` et le
  `track` épinglés sont conservés.
- **Un service ne doit pas entrer en collision avec un ancien app
  autonome.** Le compilateur refuse explicitement (`apps/<service>` existe
  encore) plutôt que de laisser les deux revendiquer le même hôte.
- **Le `values.yaml` reste la seule porte de sortie des artefacts.** Le
  manifeste décrit l’intention, le digest est écrit par le control plane.
- **Une app peut être déclarée sans `repository`.** Elle reste dans le
  GitOps, jamais construite, et n’est activée qu’après le premier pin.
- **Le webhook Forgejo du dépôt applicatif** doit être créé et retiré en même
  temps que l’app dérivée du manifeste, sinon il reste orphelin.

---

## 5. Les addons

Les addons sont les dépendances d’infrastructure d’un projet. Avant, un seul
type existait (PostgreSQL), écrit en dur dans le compilateur, et rien ne
reliait un addon à un service.

### Les moteurs sont pluggables

Un moteur (`AddonEngine`) écrit les objets Kubernetes d’un addon et déclare ce
que les services consomment. Le registre est le seul point d’entrée : ajouter
un type d’addon est ajouter un moteur, pas modifier le compilateur.

| Moteur | Rendu | Service |
| --- | --- | --- |
| `cnpg` | `Cluster` CloudNativePG | `DATABASE_URL` (Secret `uri` généré par CNPG) |
| `valkey` | `StatefulSet` + `Service` | `VALKEY_URL` |

Un moteur inconnu est refusé, et l’erreur liste ceux qui existent.

### Un service consomme un addon

```yaml
services:
  site:
    repository: pepe/site
    addons: [postgres, cache]
```

Le service reçoit l’environnement des addons qu’il utilise, et la
`NetworkPolicy` du projet l’autorise à les joindre. La valeur d’un secret ne
transite jamais par Kuberpack : seul le `secretKeyRef` est écrit.

### Pièges

- **Les pairs réseau sont structurés.** Le chart ne devine plus le port
  d’un peer : les pairs d’addons portent leur kind, leur projet et leur
  port (`networkPolicy.addonPeers`).
- **Le chart du dépôt GitOps doit être celui du control plane.** Il valide
  les valeurs avec son chart local, mais Flux applique celui du dépôt : un
  chart obsolète ignorait silencieusement les valeurs. Le control plane
  écrit donc son chart dans le dépôt au démarrage.
- **Un addon et un service ne doivent pas se déclarer deux fois.** Les pairs
  sont dédupliqués.

---

## 6. Tests et gate de release

### Implémentation

La recette commune est [test/run.py](test/run.py), exécutée dans Docker sur
le poste du développeur et dans GitHub. Les opérations critiques de création,
d’installation et de destruction des fixtures sont orchestrées avec Ansible.
Les anciens scripts Bash du banc partagé sont retirés.

| Niveau | Budget des contrôles | Scénarios |
| --- | ---: | --- |
| `static` | < 60 s | formatage, analyse Go, workflows, Ansible, charts et contrats du harnais |
| `unit-light` | < 60 s | tests Go hors réseau, sans cache de résultat |
| `unit-heavy` | < 300 s | courses Go, admission CEL réelle, RBAC et trafic réseau |
| `unit-superheavy` | < 1 800 s, cible 20–30 min | installation interrompue puis reprise, forge et registre HTTPS, build et scan réels, SBOM, Flux, secrets, reloader, webhook, protection des PVC et réinstallation |

Chaque run possède ses noms, labels, réseau, volumes, cluster et CA. Aucun
port n’est publié sur l’hôte et aucun credential du poste n’est transmis.
Les diagnostics précèdent le nettoyage, avec rapports JSON/JUnit même en cas
d’échec. Le coût initial des outils et de compilation est rapporté séparément.

La CI réutilise `.github/workflows/test-suite.yaml`. Statique et léger
précèdent le lourd ; le très lourd suit sur les pushes, tags, nightly et
lancements manuels. La publication des images attend les quatre niveaux.
Les PR exécutent les trois premiers niveaux.

### Validation

Les niveaux statique, léger et lourd ont passé localement et sur GitHub.
Dix passages légers consécutifs ont passé sans échec. Le parcours complet
reste en qualification jusqu’à un passage local et GitHub intégralement vert.
Les commandes, les mesures et les limites sont documentées dans
[docs/testing.md](docs/testing.md).

### Extensions distinctes

L’upgrade N-1→N et son rollback, la matrice Calico/Cilium, EKS/GKE/AKS,
les addons, previews, backup/restore, charge, ACME public et arm64 restent à
faire. Un nightly qui exécute le parcours courant ne constitue pas une
validation de ces scénarios.

---

## Hors périmètre pour l’instant

Décisions prises, pour ne pas les rediscuter à chaque fois :

| Sujet | Position |
| --- | --- |
| Logs de l’application | hors rôle de Kuberpack : le cluster de production a ses propres logs |
| Rollback | géré par Git (`revert` du commit de pin) ; Helm a déjà son `remediation.rollback` |
| Environnements / secrets | Infisical est installé et branché : le manifeste déclare ses secrets, la plateforme écrit l'`InfisicalStaticSecret` et l'opérateur synchronise le Secret Kubernetes. `prod` + preview de PR restent les seuls environnements |

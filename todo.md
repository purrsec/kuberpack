# À faire

Chantiers ouverts, du plus utile pour le développeur au plus structurel. Ils ne
sont pas écrits dans le code : ce document fixe le contrat visé, l’endroit où
le travail tombe, et ce qui vaut dire « fait ».

| # | Chantier | Statut |
| --- | --- | --- |
| 1 | [Logs de build](#1-logs-de-build) | à faire |
| 2 | [Dimensionnement CPU/RAM](#2-dimensionnement-cpuram) | à faire |
| 3 | [CLI de pilotage](#3-cli-de-pilotage) | à faire |
| 4 | [Le manifeste pilote le déploiement](#4-le-manifeste-pilote-le-déploiement) | en cours |

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

### Étape 2 — le manifeste arme le pipeline (à faire)

C’est le cœur : aujourd’hui `services.*.repository` est **inerte** — le
manifeste compile du GitOps, mais le build ne part que si l’app a aussi été
enregistrée via `POST /api/v1/apps`.

Le compilateur doit produire, en plus des fichiers GitOps, la **description
des apps** (repository, branche, stratégie, overrides de build). Le control
plane les réconcilie alors dans SQLite et pose les webhooks Forgejo.

| Élément | Emplacement |
| --- | --- |
| Sortie du compilateur | `internal/promote/project.go` (le résultat porte les apps) |
| Réconciliation | `internal/control/projects.go` (store + client Forgejo) |
| App record | `internal/store` (l’app devient une projection du manifeste) |
| Webhook | `internal/forgejo` (idem qu’à l’enregistrement actuel) |

### Étape 3 — l’API d’enregistrement disparaît (à faire)

`POST/PATCH/DELETE /api/v1/apps` font doublon avec le manifeste : deux
écrivains pour la même chose, donc deux sources de vérité. Les supprimer, ou
les garder comme **éditeurs de manifeste** (ils écrivent le `kuberpack.yaml`
puis le compilent) — c’est le même arbitrage, à trancher.

`GET /api/v1/apps/{name}/builds` reste : c’est de l’état opérationnel.

### Étape 4 — le chemin Flux des projets (à faire)

Le `Kustomization` Flux watche `./kubernetes/vps/apps`, alors que les projets
vivent dans `./kubernetes/vps/projects` : **les projets compilés ne sont
jamais réconciliés**. Il faut un second `Kustomization` Flux, ou un chemin
qui couvre les deux.

### Pièges

- **La compilation doit rester idempotente.** Un projet recompilé ne doit
  jamais toucher un `values.yaml` déjà épinglé (règle d’or n°2).
- **Le `values.yaml` reste la seule porte de sortie des artefacts.** Le
  manifeste décrit l’intention, le digest est écrit par le control plane.
- **Une app peut être déclarée sans `repository`.** Elle reste dans le
  GitOps, jamais construite, et n’est activée qu’après le premier pin.
- **Le webhook Forgejo du dépôt applicatif** doit être créé et retiré en même
  temps que l’app dérivée du manifeste, sinon il reste orphelin.

---

## Hors périmètre pour l’instant

Décisions prises, pour ne pas les rediscuter à chaque fois :

| Sujet | Position |
| --- | --- |
| Logs de l’application | hors rôle de Kuberpack : le cluster de production a ses propres logs |
| Rollback | géré par Git (`revert` du commit de pin) ; Helm a déjà son `remediation.rollback` |
| Environnements / secrets | plus tard, avec Infisical ; `prod` + preview de PR suffisent aujourd’hui |

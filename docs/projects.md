> Le contrat courant est l’[API v2](api.md) et le [guide CLI](cli.md). La réservation et la destruction relèvent de `kuberpack-admin` ; `init` et `deploy` sont interactifs sans flags obligatoires. Les anciens exemples de `project create/apply` de ce document décrivent le compilateur historique et ne sont plus des commandes disponibles.

# Projets — manifeste et compilateur

Statut : **proposition / première tranche en cours**. Décrit un manifeste humain compilé
vers les primitives GitOps existantes.

## Idée

Un **projet** décrit un ensemble de services et d'addons dans un fichier lisible :

```yaml
# kubernetes/vps/projects/mitame/kuberpack.yaml
project: mitame
namespace: mitame
services:
  site:
    type: stateless
    repository: pepe/site-2025
    port: 8080
    hostname: mitame.host.bzh
  umami:
    type: stateless
    repository: pepe/umami
    port: 8080
    hostname: umami.host.bzh
    secrets: [UMAMI_APP_SECRET]
addons:
  postgres:
    engine: cnpg
    database: umami
  valkey: {}
policies:
  defaultDeny: true
  internet: true
  peers: [postgres, valkey]
```

Le control plane **compile** ce fichier vers les primitives déjà produites aujourd'hui :
`HelmRelease` + `values.yaml` (chart `stateless`), `Cluster` CNPG, `NetworkPolicy`,
`InfisicalStaticSecret`.

## Le fichier manifeste est la source ; le reste est généré

- `kuberpack.yaml` : écrit par un humain (ou le wizard CLI).
- `helmrelease.yaml`, `values.yaml`, `kustomization.yaml`, `database.yaml`, CR Infisical :
  **générés** par le compilateur.

## Propriété des champs (règle dure)

| Champ | Propriétaire |
| --- | --- |
| structure (services, ports, hostnames, addons, policies) | le **manifeste** |
| `image`, `track` | le **control plane** (promote / digest) |

Le compilateur **ne réécrit jamais** `image` ni `track` : il les préserve s'ils existent.
Un service sans `image` pinnée n'est pas activé dans Flux (comportement actuel).

## Où ça tourne

Dans le **control plane**. La CLI est un assistant + un client HTTP :

```
CLI (wizard, --dry-run)  ──POST /api/v1/projects──▶  control plane
                                                       │ compile
                                                       ▼
                                              écrit le GitOps + commit
                                                       ▼
                                                     Flux
```

Un seul écrivain Git (le control plane). Pas de credentials GitOps côté développeur.
En `--dry-run`, la CLI importe le **même package Go** pour afficher le diff sans écrire.

## Contrat API (première tranche)

```http
POST /api/v1/projects            # {project, manifest: "<yaml>"} -> compile + commit
GET  /api/v1/projects            # liste
GET  /api/v1/projects/{name}     # manifeste (YAML)
DELETE /api/v1/projects/{name}   # retire du parent, supprime le dossier, prune
```

## Compilation (première tranche)

| Manifeste | Sortie |
| --- | --- |
| `services.<name>.type: stateless` | `projects/<p>/<name>/` : HelmRelease + values + kustomization |
| `addons.postgres.engine: cnpg` | `projects/<p>/postgres/` : `Cluster` CNPG (+ `NetworkPolicy`) |
| `policies.peers` | `networkPolicy.peers` dans chaque service |
| `services.<name>.secrets` | CR `InfisicalStaticSecret` (`app-<p>-<name>`) |
| parent | `projects/<p>/kustomization.yaml` listant les services pinnés et les addons |
| parent des projets | `projects/kustomization.yaml` listant les projets |

`namespace` du manifeste : utilisé pour `namespace:` de la kustomization du projet.
(Première tranche : namespace unique par projet ; les addons restent dans ce namespace.)

## Hors périmètre (première tranche)

- `valkey` (StatefulSet) — postgres d'abord.
- Backups (`backup: { s3: garage }`) — motif CNPG connu, à câbler ensuite.
- Previews par service de projet.
- Détection de dérive source ↔ généré en CI.

## CLI

```bash
kuberpack                 # assistant de tâches
kuberpack init            # formulaire de préparation locale
kuberpack deploy          # plan puis publication du manifeste complet
kuberpack services list
kuberpack redeploy        # reconstruction d’un service enregistré
```

Le wizard n'utilise **aucune UI** : prompts texte, validation, puis POST.

### Le manifeste et les dépendances

`kuberpack init` propose PostgreSQL et Valkey dans ses réglages avancés. Les autres moteurs disponibles se déclarent dans le manifeste complet. L'opérateur doit avoir installé les opérateurs nécessaires ; le plan vérifie les capacités de la plateforme.

`kuberpack deploy` publie le manifeste complet après présentation du plan. Les dépendances persistantes ne se suppriment pas par omission : une migration opérateur est requise. `validate` contrôle la structure locale, et `deploy --plan` vérifie l'état distant sans push ni build.

Les détails de l'assistant, des permissions, de l'attente et de l'automatisation sont dans [le guide CLI](cli.md).

# Format du manifeste Kuberpack (`kuberpack.yaml`)

Ce document décrit la spécification du fichier de configuration compact `kuberpack.yaml`, conçu pour déclarer un projet complet (services applicatifs, bases de données, politiques réseau) de manière lisible, sans boilerplate Kubernetes ni manipulation manuelle de manifests Flux.

---

## 1. Philosophie et objectifs

L'ambition de Kuberpack est d'offrir une **expérience PaaS fluide** (type Railway / Coolify) tout en conservant une **architecture 100 % GitOps et sans UI lourde** :

- **Source de vérité unique pour l'humain :** Un seul fichier YAML compact (`kuberpack.yaml`) décrit l'architecture souhaitée pour un projet.
- **Zéro friction Kubernetes :** Le développeur n'écrit ni `Deployment`, ni `Service`, ni `Ingress`, ni `HelmRelease`, ni `NetworkPolicy`, ni `Kustomization`.
- **Compilateur déterministe :** Le control plane Kuberpack prend ce manifeste en entrée et compile l'arborescence GitOps complète consommée par Flux CD.
- **Sécurité et secrets hors Git :** Les clés de secrets nécessaires sont déclarées sous forme de références ; aucune valeur sensible ne transite par Git ou SQLite.

---

## 2. Exemple de référence

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
    healthcheck: /healthz
    replicas: 1

  umami:
    type: stateless
    repository: pepe/umami
    port: 8080
    hostname: umami.host.bzh
    secrets:
      - UMAMI_APP_SECRET

addons:
  postgres:
    engine: cnpg
    database: umami
    backup:
      retention: 30d

policies:
  defaultDeny: true
  internet: true
  peers:
    - postgres
```

---

## 3. Spécification détaillée des champs

### 3.1. Métadonnées du projet

| Champ | Type | Requis | Description |
|---|---|---|---|
| `project` | string | Oui | Identifiant unique du projet (utilisé pour les dossiers GitOps et le nommage des ressources). |
| `namespace` | string | Non | Namespace Kubernetes cible. Par défaut égal au nom du `project`. |

---

### 3.2. Section `services`

Chaque entrée de la table `services` définit une application exécutable.

```yaml
services:
  <nom-du-service>:
    type: stateless          # type de workload (stateless par défaut)
    repository: <owner/repo> # dépôt Git Forgejo/GitHub source
    branch: main             # branche dont les pushes deviennent la production
    strategy: auto           # auto | uv | railpack
    port: 8080               # port d'écoute HTTP du conteneur (défaut : 8080)
    hostname: <fqdn>         # nom d'hôte public configuré sur l'Ingress
    healthcheck: /healthz    # chemin HTTP utilisé pour la liveness/readiness probe (défaut : /)
    replicas: 1              # nombre d'instances (défaut : 1)
    secrets:                 # liste des clés de secrets Infisical à synchroniser
      - API_KEY
    secret_env: prod         # environnement Infisical des secrets (défaut : prod)
    env:                     # variables d'environnement statiques (optionnel)
      NODE_ENV: production
    build:                   # options spécifiques de build Railpack (optionnel)
      root_directory: ""     # sous-dossier en cas de monorepo
      build_command: ""      # commande personnalisée de build front/asset
      start_command: ""      # commande de démarrage (défaut : détectée par Railpack)
```

#### Types de services supportés :
- `stateless` (par défaut) : Workload HTTP long (API, site SSR, SPA servie par Caddy via Railpack). Déployé via le chart Helm `stateless` Kuberpack.
- `worker` (futur) : Tâche de fond non exposée publiquement (consommateur de files, cron/daemon). **Pas encore supporté** : le compilateur refuse un manifeste qui le déclare.

---

### 3.3. Section `addons`

Les `addons` sont des dépendances d'infrastructure managées automatiquement pour le projet (pas de code source associé).

```yaml
addons:
  postgres:
    engine: cnpg             # moteur PostgreSQL (CloudNativePG)
    database: umami          # nom de la base de données créée
    backup:
      retention: 30d         # durée de rétention des sauvegardes (CNPG retentionPolicy)
```

Seul `engine: cnpg` est supporté : le compilateur refuse tout autre moteur.
`backup.s3` n'est pas encore supporté (le profil de stockage est une décision
d'infrastructure) : il est refusé explicitement. `backup.retention` est rendu
dans le `Cluster` CNPG.

---

### 3.4. Section `policies`

Déclare les contraintes de sécurité et de filtrage réseau via Kubernetes `NetworkPolicy`.

```yaml
policies:
  defaultDeny: true          # isole les services du projet (défaut : true)
  internet: true             # autorise (true) ou bloque (false) l'accès sortant vers le web
  peers:                     # liste des services ou addons locaux joignables
    - postgres
```

- **`defaultDeny`** : isole les services du projet. `true` (défaut) rend la `NetworkPolicy` du chart, `false` la désactive : le service n'est plus cloisonné.
- **`internet`** : Permet aux services d'effectuer des appels externes (APIs tierces, webhooks, passerelles de paiement).
- **`peers`** : Configure les règles ingress/egress des `NetworkPolicy` pour autoriser le trafic inter-services et vers les addons déclarés. Chaque peer doit être **un service ou un addon du projet** : le compilateur refuse un nom inconnu.

---

## 4. Séparation stricte des responsabilités

Kuberpack impose une séparation étanche entre ce qui est sous le contrôle de l'humain et ce qui relève de l'automatisation du control plane :

| Responsabilité | Propriétaire | Emplacement |
|---|---|---|
| **Intention & Architecture** (services, ports, hostnames, addons, règles réseau, liste des clés de secrets) | **Humain** (ou wizard CLI) | `projects/<projet>/kuberpack.yaml` |
| **Artefacts d'exécution** (tags immuables d'images `sha-…@sha256:…`, digests vérifiés, branche de suivi `track`) | **Control plane Kuberpack** | `projects/<projet>/<service>/values.yaml` |
| **Synchronisation d'infrastructure** | **Flux CD** | Cluster de production |

### Règles d'or :
1. **Le développeur n'écrit jamais de SHA ou de digest d'image** dans `kuberpack.yaml`.
2. **Le compilateur ne réécrit jamais `image` ni `track`** lorsqu'il régénère les manifests d'un service existant : les versions publiées sont préservées.
3. Un service déclaré dont l'image n'a pas encore été construite par Kuberpack reste présent dans GitOps mais n'est **pas activé dans Flux** tant que le premier build n'a pas réussi.

---

## 5. Ce que génère le compilateur Kuberpack

Lorsqu'un manifeste `kuberpack.yaml` est soumis (via l'API `POST /api/v1/projects` ou via la CLI) :

```
kuberpack.yaml (humain)
       │
       ▼
[ Compilateur Kuberpack ]
       │
       ├──▶ projects/<p>/namespace.yaml              (Namespace du projet)
       ├──▶ projects/<p>/<service>/helmrelease.yaml   (référence chart stateless)
       ├──▶ projects/<p>/<service>/values.yaml        (port, host, probes, replicas, env, peers, secrets ref)
       ├──▶ projects/<p>/<service>/kustomization.yaml (ressources du service)
       ├──▶ projects/<p>/<service>/secret.yaml        (CR InfisicalStaticSecret, si le service déclare des secrets)
       ├──▶ projects/<p>/postgres/database.yaml        (CR Cluster CloudNativePG)
       └──▶ projects/<p>/kustomization.yaml           (kustomization parent activant les services pinnés)
```

1. **`HelmRelease` + `values.yaml` :** Utilise le chart Helm `stateless` officiel de Kuberpack avec Ingress Traefik, annotations ExternalDNS IPv6, probes, `replicas`, `env`, et `envFrom` raccordé au Secret généré.
2. **Namespace :** le projet crée son namespace (`namespace` du manifeste, ou le nom du projet) et toutes ses ressources y vivent : `HelmRelease`, `Cluster` CNPG, Secret applicatif.
3. **Ressource `Cluster` CNPG :** Instancie la base PostgreSQL avec son pooler, ses credentials automatiques et la rétention déclarée.
4. **`NetworkPolicy` :** Traduit `policies.peers` et `policies.internet` en règles d'isolation Pod réseau natives. `policies.defaultDeny: false` désactive la NetworkPolicy du service.
5. **`InfisicalStaticSecret` :** Branche la synchronisation des secrets depuis l'instance Infisical du homelab vers un Kubernetes Secret `app-<service>` (sans que les valeurs en clair ne transitent par Kuberpack). L'objet généré est nommé `app-<projet>-<service>` pour que deux projets puissent déclarer un service du même nom.

### Le cycle complet

Le manifeste vit dans le dépôt GitOps, à
`kubernetes/vps/projects/<projet>/kuberpack.yaml`. Kuberpack y pose un
webhook : **un push sur ce dépôt recompile les projets touchés**.

```
développeur            Forgejo              Kuberpack             Flux
     │                    │                     │                   │
     │ édite le manifeste│                     │                   │
     ├───── push ────────►│                     │                   │
     │                    ├─ webhook ─────────►│                   │
     │                    │                     │ compile            │
     │                    │                     ├─ push GitOps ─────►│
     │                    │                     │                     ├─ apply
```

Le manifeste est lu depuis le dépôt et **jamais réécrit** : les commentaires
du développeur survivent, et le contrôleur de version fait foi. Le statut de
commit.publish sur le push (`1 project(s) compiled`, ou l'erreur de
compilation) dit au développeur si son manifeste est valide.

`POST /api/v1/projects` reste disponible : c'est la même compilation, avec
le manifeste fourni par l'API. Les deux chemins écrivent la même chose.

Un push qui supprime un manifeste supprime le projet : l'app, son webhook et
son secret disparaissent.

### Ce que fait la compilation

Pour chaque service qui déclare un `repository`, la compilation :

1. écrit les artefacts GitOps du service ;
2. inscrit l'app (`<projet>-<service>`), sa clé HMAC et son webhook Forgejo ;
3. enfile le premier build.

Le `values.yaml` est **régénéré** à chaque compilation : le manifeste est la
source de vérité du runtime. Seuls l'`image` et le `track` épinglés par le
control plane sont conservés — un changement de hostname, de `replicas` ou
d'`env` dans le manifeste se répercute donc sur le déploiement, sans rebuild.

Un service qui quitte le manifeste perd son app, son webhook et son secret.

### Flux : un `Kustomization` par arbre

Les projets vivent dans `kubernetes/vps/projects`, les apps historiques dans
`kubernetes/vps/apps`. Flux a besoin d**un `Kustomization` par arbre** : sans
un second `Kustomization` sur `./kubernetes/vps/projects`, un projet compilé
n'est jamais appliqué.

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
  sourceRef:
    kind: GitRepository
    name: infra-homelab
```

### Le compilateur refuse ce qu'il ne sait pas faire

Un manifeste est parsé **strictement** : un champ inconnu est une erreur, pas un
no-op silencieux. Le compilateur refuse explicitement :

| Refus | Raison |
|---|---|
| champ inconnu (`publish_directory`, …) | il n'existe pas de implémentation ; le champ serait ignoré en silence |
| `type` autre que `stateless` | le type `worker` n'existe pas encore |
| `engine` d'addon autre que `cnpg` | seul CloudNativePG est supporté |
| `backup.s3` | le profil de stockage S3 n'est pas décrit |
| `peers` qui ne désigne ni un service ni un addon du projet | la règle n'aurait aucun effet |
| `strategy` hors `auto`/`uv`/`railpack` | stratégie inconnue |
| `repository` qui n'est pas `owner/name` | le dépôt serait introuvable |

---

## 6. Expérience Développeur (CLI & Workflows)

Pour manipuler ce format sans manipulation de fichiers manuelle, la CLI fournit un assistant texte interactif :

```bash
# Lance un wizard interactif (prompts texte simples, aucune interface graphique)
kuberpack project create

# Simule la compilation et affiche le diff GitOps produit sans écrire
kuberpack project create --dry-run

# Supprime proprement un projet (retrait du parent Flux, suppression des dossiers et pruning)
kuberpack project delete --project mitame
```

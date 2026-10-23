# Installer Kuberpack sur un Kubernetes existant

`kuberpack install` orchestre des étapes Go avec les bibliothèques Kubernetes
et Helm. Le CLI embarque ses charts : aucun checkout du produit, Ansible,
Python ou Helmfile n'est nécessaire pour installer Kuberpack. Git doit être
présent sur la machine qui exécute le CLI.

## Préparer le cluster

L'installateur utilise le kubeconfig courant, ou `--kubeconfig` et `--context`.
Il faut les droits d'administration nécessaires aux CRD et aux opérateurs,
une StorageClass utilisable, un contrôleur d'ingress et des nœuds Linux
compatibles avec les builds BuildKit rootless. Le domaine doit pointer vers
l'exposition de cet ingress. Kuberpack ne crée ni VPC, ni cluster EKS/GKE,
ni contrôleur réseau ou disque propre au fournisseur.

La cible est un Kubernetes existant, notamment EKS, GKE ou k3s. Les tests
réels utilisent k3s Docker et un [VPS recréé de 8 Go](install-vps-2026-10-22.md) ;
cela ne certifie pas tous les modes Autopilot,
Fargate, les politiques d'admission ou toutes les versions Kubernetes.
Les NetworkPolicy nécessitent un plugin réseau qui les applique.

Deux identifiants distincts sont fournis par l'opérateur :

- `KUBERPACK_GIT_TOKEN` : lecture du code, gestion des webhooks et écriture
  dans le dépôt GitOps. Kuberpack n'écrit pas dans les dépôts applicatifs.
- `KUBERPACK_REGISTRY_TOKEN` : publication et lecture des images. Le builder
  l'utilise pour pousser ; le control plane l'utilise pour les Secrets de pull.

Les dépôts GitOps et applicatifs existent déjà. Le registre peut être
indépendant de la forge (`--registry-url`, `--registry-user`). Les identifiants
cloud temporaires d'un registre nécessitent une stratégie de renouvellement
adaptée à ce fournisseur ; leur rotation automatique n'est pas implémentée.

## Installer

Sans argument dans un terminal, `kuberpack install` lance le questionnaire.
Pour une installation reproductible ou en CI :

```bash
kuberpack install \
  --cluster production \
  --gitops-url https://github.com/equipe/socle.git \
  --gitops-apps-url https://github.com/equipe/applications.git \
  --host kuberpack.exemple.org \
  --acme-email ops@exemple.org
```

L'IngressClass est détectée quand elle est unique. Le namespace du contrôleur
est détecté pour Traefik et ingress-nginx. `--ingress-class` et
`--ingress-namespace` permettent de sélectionner explicitement un contrôleur
existant. Les contrôleurs cloud à accès direct aux pods, comme ALB/GCE,
nécessitent une intégration réseau adaptée et ne sont pas certifiés par ce test.
`--storage-class` sélectionne un stockage ; sinon le défaut du cluster est
utilisé. Pour les certificats, choisir un `--issuer` existant ou fournir
`--acme-email` pour créer l'issuer ACME dédié à Kuberpack.

En environnement local, `--tls=false` conserve l'ingress HTTP ;
`--ingress=false --tls=false --infisical=false --node-port 30080` permet
un control plane sans exposition par ingress. Les adresses privées de forge
ou de registre peuvent nécessiter `--allow-private-build-egress`.
`--ca-file` fournit la CA de la forge au clone initial et à Flux ; les nœuds
et les images de build doivent également faire confiance à ce registre.

`--file plateforme.yaml` charge les choix non secrets, avec validation stricte.
Les flags explicites priment sur ce fichier. Ne pas y mettre de jetons.
Fournir des credentials distincts via `KUBERPACK_GIT_TOKEN` (écriture GitOps et
API de forge), `KUBERPACK_REGISTRY_TOKEN` (images seulement) et, pour des sources
privées, `KUBERPACK_BUILDER_GIT_TOKEN` (lecture seulement). Ce dernier peut être
omis pour les dépôts publics. L’installateur refuse un même jeton pour plusieurs
de ces rôles. Une clé API séparée est générée si elle n’est pas fournie.
Des chaînes différentes ne prouvent pas les scopes : les permissions réelles
doivent être limitées chez le fournisseur. Voir [sécurité](security.md).

Les images peuvent être sélectionnées avec `--image`, `--builder-image` et
`--buildkit-image` ; utiliser une version publiée correspondant au CLI,
épinglée par digest en production. Les règles d’admission du builder exigent
Kubernetes 1.30 ou plus récent et sont activées par défaut. Les NetworkPolicies
des projets nécessitent un CNI qui les applique effectivement. L'installateur
détecte le moteur réseau et affiche ses preuves ; si le moteur est inconnu ou
désactivé, il avertit explicitement. Après l'installation, vérifier le trafic
réel avec `kuberpack doctor network --context <contexte> --yes` (namespace
temporaire, aucun Secret lu) et consulter la
[matrice réseau](network-matrix.md).
L'installateur prépare les Secrets de pull du control plane et du builder
avec les identifiants du registre fourni, y compris pour leur premier
démarrage sur des nœuds sans images préchargées.
Les limites mémoire initiales sont de 3 Gio pour le builder et son scan,
et de 4 Gio pour BuildKit. Elles sont réglables avec `--builder-memory` et
`--buildkit-memory` selon les nœuds disponibles.

Le placement des builds est optionnel : `--builder-runtime-class` applique un
RuntimeClass (gVisor, Kata) au Pod du build, `--builder-node-selector` et
`--builder-tolerations` reçoivent du JSON pour réserver un pool de nœuds tainté.
Vide, le comportement reste inchangé : runtime par défaut et nœuds `amd64`
quelconques. L’installateur valide ces valeurs mais ne crée ni runtime, ni
pool, ni taint. Voir [Isoler les builds](build-isolation.md).

## Les étapes

1. Inspecter le cluster et vérifier que le dossier GitOps est dédié à cette
   installation et à l'identité de ce cluster.
2. Préparer les namespaces et les Secrets. Un jeton d'API existant est
   conservé ; les mots de passe et clés de chiffrement ne sont pas régénérés.
3. Réutiliser Flux, ou installer sa version épinglée avec contrôle du SHA-256
   des manifests téléchargés. Un Flux partagé n'est pas mis à niveau.
4. Réutiliser les opérateurs présents, ou installer cert-manager,
   CloudNativePG et son plugin de sauvegarde Barman.
5. Installer Infisical et son opérateur, créer le projet de secrets, une
   identité Kubernetes de lecture dédiée et l'accès du compte humain initial.
6. Publier le socle dans Git et confier ses releases à Flux.
7. Attendre les réconciliations et vérifier la santé et l'authentification API.

Les étapes relisent l'état réel à chaque exécution. Une Lease empêche deux
installateurs de modifier la même installation simultanément. Après une
interruption, relancer la même commande. Une installation Helm interrompue
est reprise ; le hook initial Infisical peut être rejoué si nécessaire.

Les identifiants restent dans les Secrets Kubernetes. La sortie indique où
récupérer le jeton d'API et les identifiants initiaux Infisical ; elle n'affiche
pas leurs valeurs. Le profil CLI est aussi enregistré localement en mode
0600 dans `~/.config/kuberpack/clusters/<cluster>.env`, sans écraser un profil
différent. La sortie donne la commande pour l'activer. La sauvegarde des
Secrets et des volumes reste nécessaire.
`install` ne constitue pas une commande de rotation ni de restauration.

`--dry-run` inspecte le cluster et décrit les étapes sans écrire dans le
cluster ou dans Git. Il ne simule pas tous les appels Helm et Infisical.

## GitOps et coexistence avec Helmfile

Avec `--gitops-apps-url`, le dépôt de la plateforme et celui des applications
sont séparés. L'installateur écrit le socle dans le premier ; le control plane
n'écrit que le second :

```text
socle/                                  # installateur uniquement
clusters/production/kuberpack/
  installation.yaml       choix non secrets et identité du cluster
  charts/                 charts Kuberpack embarqués
  platform/               releases et socle gérés par Kuberpack

applications/                           # control plane uniquement
  kuberpack-apps.yaml     identité du dépôt applicatif
  projects/               projets issus des kuberpack.yaml
```

Le chart `stateless` reste dans le dépôt du socle : les HelmRelease
applicatives le référencent par la source de la plateforme, donc le control
plane ne peut pas modifier le chart qu'il fait appliquer. Un dépôt applicatif
doit être vide (un README d'initialisation est accepté) et dédié à une seule
installation. `--gitops-apps-root` place les projets dans un sous-dossier du
dépôt applicatif ; sans lui, ils sont à la racine.

Sans `--gitops-apps-url`, le layout historique à un seul dépôt reste supporté :
la plateforme et les projets partagent `--gitops-root`. Ce mode est conservé
pour les installations existantes ; une nouvelle installation doit séparer les
deux dépôts.

Avec les deux dépôts, l'installateur active l'isolation des projets. Le control
plane crée pour chaque projet un namespace possédé, un ServiceAccount sans jeton
et une Kustomization Flux qui applique le projet avec cette identité. Le
RoleBinding est limité au ClusterRole `kuberpack-tenant`; des
ValidatingAdmissionPolicy bornent ce que le control plane peut créer. Le
namespace n'est plus écrit dans le dépôt applicatif.

Sur une installation historique à un seul dépôt, l'ancienne Kustomization
globale des projets est suspendue, jamais supprimée avec `prune` : la supprimer
effacerait les namespaces et les workloads de son inventaire. Les projets déjà
compilés sont repris par le control plane au démarrage suivant.

`--gitops-root` change le périmètre du socle. La racine du dépôt n'est jamais
ajoutée à une réconciliation globale. Une seconde Kustomization du même chemin
est refusée. Le socle utilise `prune: false` pour préserver ses volumes et les
composants partagés ; les projets disposent de leur propre périmètre de prune.
Retirer un composant du socle demande donc une opération explicite.

Un dépôt applicatif peut contenir simultanément :

```text
mon-projet/
  kuberpack.yaml           services avec l'abstraction Kuberpack
  helmfile.yaml            releases complexes gérées directement par Helmfile
  charts/                 charts personnalisés
```

`project compile` lit seulement le manifeste demandé. Il ne lance pas
Helmfile et ne réécrit pas ses fichiers. `helmfile sync` ou votre CI conserve
la responsabilité de ces releases. Pour une release donnée, choisir un seul
responsable : Helmfile ou Kuberpack/Flux. Les services nécessaires déjà gérés
ailleurs sont réutilisés, sans changement de leurs valeurs ni de leur version.
Éviter de placer vos propres fichiers dans les répertoires générés ci-dessus.

## Après l'installation

Configurer le CLI avec l'URL indiquée et son jeton d'API. Le développeur
n'a ensuite besoin d'aucun kubeconfig :

```bash
infisical login --domain https://infisical.exemple.org
# Charger les secrets dans le projet indiqué par l'installation.
kuberpack project init
kuberpack project compile
kuberpack apps
```

L'accès humain initial est celui de l'administrateur créé par le bootstrap.
L'attribution des accès aux autres développeurs reste une action de gestion
dans Infisical. Voir aussi l'[exemple Stirling PDF](../examples/stirling-pdf/README.md).

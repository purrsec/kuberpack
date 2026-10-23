# Sécurité

## Frontières de confiance

Kuberpack gère les déploiements au nom d’un administrateur. Les namespaces et
NetworkPolicies isolent les workloads. La clé API globale reste administratrice ;
des **jetons limités à un projet** peuvent la remplacer pour un usage restreint
(CI, développeur), sans isoler pour autant les identités ou chemins Infisical
(risque 2 de l’audit). L’[audit du 23 octobre 2026](security-audit-2026-10-23.md)
distingue les protections vérifiées des risques restant à traiter.

| Credential | Usage attendu | Interdit |
| --- | --- | --- |
| `KUBERPACK_GIT_TOKEN` | API de forge et écriture du GitOps configuré | Montage dans le builder, clé API Kuberpack |
| `KUBERPACK_BUILDER_GIT_TOKEN` | Lecture des sources privées ; optionnel pour les sources publiques | Écriture des sources ou du GitOps |
| `KUBERPACK_REGISTRY_TOKEN` | Push/pull des images autorisées | Écriture Git, administration Kubernetes |
| `KUBERPACK_API_TOKEN` | Administration de Kuberpack | Réutilisation d’un credential de forge/registre |
| Jeton API de projet (`kp_...`) | Lecture et pilotage d’un seul projet | Administration, lecture des autres projets |

L’installateur refuse les credentials identiques. Les builds refusent par défaut
un credential correspondant au CP. Cela ne prouve pas les scopes réels d’un
second PAT : les permissions chez le fournisseur doivent être limitées.
Un credential d’archives peut être fourni dans la clé builder `builds-token`.

Un clone public est anonyme ; un clone privé doit appartenir à l’origine de son
provider. Son header Git est limité à cette origine, sans redirection. Le
registre utilise son propre credential, sans repli vers le token GitOps.

Commits signés sur `main` si le dépôt l’exige : clé du bot, pas d’un admin humain.

Les bots de dépendances (Renovate, etc.) ne gèrent pas les images que Kuberpack promeut.

## Webhooks

- HMAC par application, hors SQLite ;
- signature sur le **body brut** (`X-Forgejo-Signature` / `X-Gitea-Signature`, SHA-256) — voir [git.md](git.md) ;
- taille de body limitée ;
- `push` et `pull_request` seulement ;
- déduplication par identifiant de livraison ;
- file asynchrone ;
- logs sans credential émis par Kuberpack ; les logs applicatifs peuvent
  néanmoins contenir une valeur que l’application y imprime.

La clé API est comparée en temps constant, avant lecture du corps. Sans clé
explicite, les routes administratives restent fermées. Les corps JSON/webhooks
et les lectures de logs sont bornés ; les streams ont un plafond de connexions
et un délai d’écriture.

## Jetons API

`KUBERPACK_API_TOKEN` reste le jeton administrateur global. Des jetons
supplémentaires, administrateurs ou limités à un projet, vivent dans la table
`api_tokens` : nom, projet, rôle, dates de création, de dernière utilisation,
d’expiration et de révocation. Seule l’empreinte **SHA-256** du secret est
stockée ; le jeton `kp_<id>_<secret>` n’est affiché qu’à la création, jamais
dans une liste, une réponse ultérieure ou un journal. La comparaison se fait en
temps constant sur l’empreinte, puis l’état du jeton est vérifié ; un jeton
inconnu, expiré ou révoqué répond `401` sans distinction.

Un jeton de projet ne voit que les apps de son projet et ne peut compiler ou
supprimer que ce projet ; les routes `/api/v1/tokens` restent réservées à
l’administrateur. Une app d’un autre projet répond `404`. **Limites** : le jeton
ne crée pas d’isolation Infisical (projet/chemin et identité partagés, risque 2),
ne change pas les webhooks HMAC par application, et il n’existe ni quota ni
anti-rejeu par jeton. Un jeton administrateur créé par l’API a les mêmes
pouvoirs que `KUBERPACK_API_TOKEN` ; sa compromission vaut celle du jeton global.

Un jeton de projet autorise la réécriture du manifeste de **son** projet
(namespace, services, addons, politiques). Ce n’est donc pas une frontière
contre un auteur de manifeste hostile : il ne restreint ni les dépôts sources,
ni les images, ni les chemins Infisical que ce manifeste référence, et il ne
remplace pas la séparation d’identités prévue par le risque 2.

## Pull requests

Code non fiable : pas de secret de production, pas de token large, pas de socket Docker, pas de ServiceAccount privilégié. Isolation rootless. Forks refusés par défaut.

## Runtime de production

- non-root ; si l’image a un USER numérique, on le garde ; les images Railpack `USER root` passent à `65534` (chart) ;
- Pod Security `baseline` pour les namespaces de projets, audit `restricted` ;
- `allowPrivilegeEscalation: false` sauf le builder ;
- seules les clés de secrets déclarées sont injectées ; pas de SDK de secrets dans l’application.

L’image CP contient le binaire Go et le support natif Git/HTTPS, sans shell,
Railpack, uv ou buildctl. UID 65532, rootfs en lecture seule, seccomp RuntimeDefault,
capabilities retirées. Git reçoit des arguments séparés et ses hooks sont
désactivés. Une image sans shell conserve les pouvoirs de ses credentials.

Le builder n’a pas de token SA, socket Docker ou hostPath. Des policies
d’admission bornent Jobs et Pods : images/commandes/arguments, UID/GID, compte
de service, volumes, credentials et absence de privilèges/namespaces hôte.
Cette protection activée par défaut exige Kubernetes >= 1.30. Les images de
production doivent être épinglées par digest ; un tag mutable affaiblit le
contrôle des images autorisées.

BuildKit rootless garde des exceptions seccomp/AppArmor et utilise
`--oci-worker-no-process-sandbox`. Cela n’est pas une sandbox forte pour du
code hostile. Un chemin opt-in existe : RuntimeClass gVisor/Kata et pool de
nœuds dédié, configurés par l’installateur et épinglés par l’admission. Il
n’est pas activé par défaut ; sans RuntimeClass, le noyau reste partagé. Voir
[Isoler les builds](build-isolation.md).

## Réseau des apps

Un projet possède son namespace. Deux manifestes ne peuvent pas le partager,
reprendre une identité d’application ou déplacer implicitement un projet vers
un autre namespace. Les namespaces réservés sont refusés ; le nom du manifeste
doit correspondre à son dossier GitOps.

Les services sont isolés en entrée et sortie. `internet` vaut **false par défaut**
pour les projets. `true` autorise HTTP/HTTPS vers le public, hors plages privées
et de traduction connues. Sans voisins ni Internet, seul le DNS est autorisé.

`services.web.peers: [api]` autorise web → api sur le port TCP déclaré par api.
La règle d’entrée correspondante est générée sur api. `services.api.peers: []`
n’autorise pas le sens inverse et n’hérite pas de `policies.peers`. La liste de
projet reste acceptée pour les anciens manifestes. `addons: [cache]` autorise
la dépendance déclarée. Un voisin appartient nécessairement au même manifeste.
`defaultDeny: false` est refusé. Ingress et test Helm propre au service ont des
autorisations spécifiques ; il n’y a plus d’ingress général entre apps.

Un refus d’entrée de namespace protège aussi les helpers. Les pods des addons
déclarent leur **egress** dans leur propre policy : DNS ; API Kubernetes et
namespace de l’opérateur du moteur quand il en existe un (`cnpg-system` pour
CloudNativePG) ; réplication entre instances du même addon sur le port du
moteur ; et, si `backup.s3` est déclaré, le stockage S3 du projet uniquement.
Un moteur sans besoin sortant n’a que le DNS : aucune règle n’ouvre
`0.0.0.0/0` ni ne dépend d’un namespace qui n’existe pas.

Les helpers (`bootstrap`, `dump`, `bucket`) ont une policy distincte : le
bootstrap ne joint que l’API et le DNS, le dump la base et le DNS, le bucket
le stockage et le DNS. Le refus d’entrée du namespace reste `Ingress` seul :
il ne coupe ni les charts gérés à la main ni l’egress des services
applicatifs, qui ont leur propre policy.

Limites natives : une NetworkPolicy ne filtre pas un nom de domaine, donc un
S3 **externe** nécessiterait un proxy ou une liste de CIDR (le S3 d’un projet
reste l’addon du même namespace). Si l’API du cluster est un endpoint hors du
réseau des pods, la règle basée sur `kube-system` peut ne pas la couvrir
selon le CNI ; ce point doit être validé sur EKS/GKE.

## Moteur réseau et preuve d'application

Les policies sont additives et exigent un CNI qui les applique réellement.
Un objet `NetworkPolicy` visible dans l'API ne prouve rien : le kubelet ne
l'applique pas. `internet: true` permet de joindre le domaine public d'un autre
projet. Les policies natives ne couvrent pas tous les flux avec le nœud ou
localhost. Les namespaces de contrôleurs externes personnalisés doivent être
exclus du provisionnement. Les Helmfiles indépendants restent sous leur propre
gestion.

`kuberpack install` détecte le moteur réseau dans `kube-system` (k3s/kube-router,
Calico, Cilium/GKE Dataplane V2, AWS VPC CNI, Azure NPM) et affiche ses preuves.
Si le moteur est inconnu, désactivé ou en mode audit, l'installateur affiche un
avertissement explicite : l'application des policies n'est pas établie.

`kuberpack doctor network --context <contexte> --yes` va plus loin : il crée
un namespace temporaire, un pod cible, des pods clients et une policy
deny-all, observe un refus réel, puis supprime le namespace. Le contexte est
obligatoire et toute probe exige `--yes`, avec un message renforcé pour un
contexte de production. La commande ne lit aucun Secret et refuse de conclure
si le contrôle positif échoue. Elle reste une preuve ponctuelle : une policy
additive peut rouvrir le réseau après le test.

La matrice détaillée (EKS VPC CNI standard/strict, GKE Dataplane V2/Calico,
k3s/kube-router), les limites et les commandes manuelles sont dans
[network-matrix.md](network-matrix.md). Le banc Docker k3s `v1.37.1+k3s1` a
exécuté `doctor network` le 23 octobre 2026 (joignable avant deny-all, bloqué
après, namespace supprimé) en plus des scénarios de `security-smoke.py`.
EKS, GKE et AKS n'ont pas été exécutés dans cette passe et restent à valider
sur leurs clusters.

## Infisical

Le web Infisical, PostgreSQL et Redis n’ont pas de token Kubernetes monté.
Le Job de bootstrap conserve celui qui lui est nécessaire. L’operator géré
utilise des Roles dans les namespaces exacts des projets et `infisical`, avec
sa liste de surveillance mise à jour par GitOps. Le manager n’a plus de
ClusterRole permettant Secrets et TokenRequests dans tout le cluster. Un
operator externe réutilisé conserve sa configuration externe.

La rotation des secrets runtime est portée par `kuberpack-reloader`, un
composant séparé avec son propre ServiceAccount. Le provisionneur lui donne un
RoleBinding dans chaque namespace de projet, limité aux Secrets et aux
Deployments de ce namespace ; le control plane ne lit aucun Secret. Le
reloader compare l’etag `secrets.infisical.com/version` du Secret consommé et
pose `kuberpack.io/secret-revision` sur le template du Deployment, ce qui
déclenche le rollout. Une application sans secret, une preview ou une
application autonome ne sont pas redémarrées. L’annotation native
`secrets.infisical.com/auto-reload` reste posée sur le Deployment comme
sélection, mais elle ne suffisait pas seule (l’operator mettait à jour le
Secret sans redémarrer le pod).

Le projet/chemin Infisical reste partagé. Un auteur de manifeste autorisé peut
y demander les noms de clés d’un autre projet. Une isolation entre développeurs
exige des projets/chemins et identités Infisical séparés, avec migration des
secrets existants. L’operator partagé garde accès aux projets gérés et au
namespace Infisical contenant les credentials de bootstrap.

## Promotion Git

Rendu des manifestes avant push. Conflit sur `main` : rebase, revalider, retry seulement si le SHA est encore HEAD.

Les symlinks dans les arbres générés sont refusés avant lecture/écriture. Noms,
chemins, quantités et scalaires sont validés contre l’injection YAML. L’observation
suit les UIDs Deployment → ReplicaSet → Pod, sans `pods/exec` ni `nodes/proxy`.

**CP → GitOps → Flux reste une frontière critique.** Le CP peut écrire dans le
dépôt qui pilote aussi la plateforme, et Flux y applique avec des droits élevés.
Une compromission du CP peut donc encore conduire à la prise du cluster. La
cible est un dépôt du socle inaccessible au CP, un dépôt applicatif réconcilié
sous des comptes limités aux namespaces et un provisionneur distinct pour
namespaces/RBAC. Les gardes dans le processus CP ne remplacent pas cette frontière.

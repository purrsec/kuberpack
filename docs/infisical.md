# Infisical sur le cluster

Infisical est le coffre de secrets de la plateforme. Kuberpack ne lit
jamais une valeur de secret : quand une application déclare `secrets`, la
plateforme génère un objet `InfisicalStaticSecret` qui fait synchroniser
les valeurs depuis l'instance vers un Secret Kubernetes. Les valeurs ne
transitent ni par le dépôt Git, ni par la base SQLite, ni par le control
plane.

L'installation suit la [notice officielle](https://infisical.com/docs/self-hosting/deployment-options/kubernetes-helm).

Le parcours courant passe par [`kuberpack install`](install.md). Il installe
les charts, crée le projet de secrets, configure une identité Kubernetes de
lecture dédiée et donne l'accès au compte humain initial. Les clés de
chiffrement et mots de passe existants sont conservés. Les sections manuelles
ci-dessous décrivent aussi le déploiement historique sur k3s ; ses valeurs
Traefik ne sont pas des prérequis universels du produit.

## Ce qu'il faut

| | |
| --- | --- |
| Un cluster Kubernetes | compatible avec les versions des composants épinglés ; voir l'installation |
| Helm | bibliothèque Go embarquée ; CLI Helm uniquement pour le parcours manuel |
| Le dépôt des charts | `helm repo add infisical-helm-charts 'https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/'` |

Deux charts sont installés :

| Chart | Rôle |
| --- | --- |
| `infisical-standalone` | l'instance, avec son PostgreSQL et son Redis |
| `secrets-operator` | l'opérateur qui synchronise les secrets (CRD `InfisicalStaticSecret`) |

## Les valeurs

Un exemple manuel k3s est dans [`infisical-values.yaml`](infisical-values.yaml).
L'installateur génère ses propres valeurs et fournit les mots de passe de
PostgreSQL et Redis par un Secret de valeurs Helm, jamais dans Git.
Trois points méritent une explication.

**Le chart embarque son propre ingress-nginx.** Le cluster a déjà Traefik :
`ingress.nginx.enabled: false` et `ingress.ingressClassName: traefik`. Deux
ingress-nginx sur le même cluster se marchent dessus.

**La mémoire d'Infisical est posée à la main.** Les migrations de schéma du
premier démarrage consomment plus de 700 Mio. Avec cette limite, le pod se
fait tuer par l'OOM killer du cgroupe, redémarre en boucle, et la boucle
sature le disque au point de geler l'API server du cluster. La limite est
à 1500 Mio, au-dessus de ce que l'instance consomme ensuite.

**Le PostgreSQL et le Redis sont ceux du chart.** C'est la voie de la
notice pour une installation de démonstration. En production, la notice
demande des bases gérées à l'extérieur ; le cluster a déjà l'opérateur
CloudNativePG, qui sait faire le même travail.

## Ce que prépare la commande install

La commande crée le namespace, les secrets, le certificat, les charts,
les comptes de service et les objets de connexion de l'opérateur.
Elle effectue également les étapes API nécessaires au raccordement.

```bash
kuberpack install \
  --git-url https://api.github.com \
  --gitops-url https://github.com/vous/deploiement.git \
  --host kuberpack.exemple.org --acme-email ops@exemple.org \
  --git-token <jeton de lecture> \
  --registry-url ghcr.io --registry-user vous \
  --registry-token <jeton de build> \
  --infisical-domain infisical.exemple.org
```

L'identité de la machine n'est pas demandée : le chart amorce l'instance
et dépose son jeton dans un Secret, la commande le lit et crée les
objets de l'opérateur avec.

Le bootstrap conserve les identifiants présents et reprend une installation
initiale interrompue. Il autorise la connexion interne à l'API Kubernetes
dans Infisical (`ALLOW_INTERNAL_IP_CONNECTIONS=true`). La rotation des
identifiants et les migrations de stockage restent des opérations explicites.

### L'installation à la main

Les étapes d'installation des charts ci-dessous sont prises en charge par
la commande. Les étapes API ci-dessous servent de référence technique ;
l'installateur les effectue pour son projet et son identité dédiés.

```bash
export KUBECONFIG=~/.kube/cluster.yaml

# 1. Le namespace et les secrets de l'instance
kubectl create namespace infisical
kubectl create secret generic infisical-secrets -n infisical \
  --from-literal=AUTH_SECRET="$(openssl rand -base64 32)" \
  --from-literal=ENCRYPTION_KEY="$(openssl rand -hex 16)" \
  --from-literal=SITE_URL="https://infisical.exemple.org"

# 2. Les identifiants d'amorçage
kubectl create secret generic infisical-bootstrap-credentials -n infisical \
  --from-literal=INFISICAL_ADMIN_EMAIL="admin@exemple.org" \
  --from-literal=INFISICAL_ADMIN_PASSWORD="un mot de passe long"

# 3. Le chart
helm repo add infisical-helm-charts \
  'https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/'
helm upgrade --install infisical infisical-helm-charts/infisical-standalone \
  --namespace infisical --values docs/infisical-values.yaml --wait
```

### La clé de chiffrement

`ENCRYPTION_KEY` chiffre les secrets dans la base. **Perdue, ils sont
perdus** : le cluster ne peut plus les déchiffrer, même si la base est
restaurée. Elle est écrite à part, hors du cluster et hors de Git :

```bash
umask 077
printf 'AUTH_SECRET=%s\nENCRYPTION_KEY=%s\n' "$AUTH" "$ENC" \
  > ~/.config/kuberpack/infisical.env
```

## L'amorçage

Le chart sait amorcer l'instance tout seul : `autoBootstrap.enabled: true`.
Le job crée l'instance, l'organisation et l'identité machine
d'administration, et dépose son jeton dans un Secret du cluster :

```bash
kubectl -n infisical get secret infisical-bootstrap-secret \
  -o jsonpath='{.data.token}' | base64 -d
```

Le Secret contient quatre clés : `token` (le jeton de l'identité machine),
`identityId`, `organizationId` et `email`.

La même chose à la main, avec le CLI :

```bash
infisical bootstrap \
  --domain="https://infisical.exemple.org" \
  --email="admin@exemple.org" \
  --password="..." \
  --organization="kuberpack"
```

Une instance ne s'amorce qu'une fois. La commande du CLI renvoie alors une
erreur : c'est normal, il faut le jeton du premier amorçage.

## Le certificat

L'instance est servie en HTTPS. Le certificat vient de cert-manager, comme
celui des applications :

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: infisical-tls
  namespace: infisical
spec:
  secretName: infisical-tls
  issuerRef:
    name: letsencrypt
    kind: ClusterIssuer
  dnsNames:
    - infisical.exemple.org
```

`SITE_URL` doit être l'URL que les clients voient. Sinon les redirections
OAuth et les cookies de session partent sur le mauvais hôte.

## L'opérateur

```bash
helm upgrade --install infisical-operator infisical-helm-charts/secrets-operator \
  --namespace infisical --wait
```

L'opérateur installe les CRD, dont `InfisicalStaticSecret` — celui que la
plateforme génère pour les applications qui déclarent des secrets.

## L'identité machine et l'opérateur

L'opérateur ne lit pas le jeton de l'identité machine : il présente un
jeton de service account du cluster, et Infisical le valide en
interrogeant l'API Kubernetes (TokenReview).

```bash
# Le service account que l'opérateur présente
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: infisical-service-account
  namespace: infisical
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: infisical-token-reviewer
  namespace: infisical
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: infisical-token-reviewer-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:auth-delegator
subjects:
  - kind: ServiceAccount
    name: infisical-token-reviewer
    namespace: infisical
EOF
```

Le jeton de confiance, celui dont Infisical se sert pour interroger
TokenReview, est un jeton de service account à durée de vie longue :

```bash
kubectl -n infisical create token infisical-token-reviewer --duration=8760h
```

La méthode d'authentification Kubernetes se crée sur l'identité machine.
Elle dit à Infisical qui a le droit de se présenter :

| Champ | Valeur |
| --- | --- |
| `kubernetesHost` | l'URL de l'API server, joignable **depuis l'instance** |
| `caCert` | le CA du cluster, pour valider le certificat de l'API |
| `tokenReviewerJwt` | le jeton du service account `infisical-token-reviewer` |
| `allowedNamespaces` | les namespaces des service accounts acceptés |
| `allowedNames` | les noms des service accounts acceptés |

L'API se pilote par `POST /api/v1/auth/kubernetes-auth/identities/{id}`.
Les routes d'identités machine ne sont pas toutes au même endroit selon
la version : la spécification OpenAPI de l'instance, à
`/api/docs/json`, fait foi.

## La connexion et l'authentification

Trois objets, dans le namespace `infisical` :

```yaml
apiVersion: secrets.infisical.com/v1beta1
kind: InfisicalConnection
metadata:
  name: infisical-connection
  namespace: infisical
spec:
  # L'adresse de base, sans le "/api" : l'opérateur ajoute le chemin
  # lui-meme. Une adresse qui finit par "/api" donne "/api/api/...".
  address: https://infisical.exemple.org
---
apiVersion: secrets.infisical.com/v1beta1
kind: InfisicalAuth
metadata:
  name: contabo-vps
  namespace: infisical
spec:
  infisicalConnectionRef:
    name: infisical-connection
    namespace: infisical
  method: kubernetes
  kubernetes:
    identityIdRef:
      name: kubernetes-credentials
      namespace: infisical
      key: identityId
    serviceAccountRef:
      name: infisical-service-account
      namespace: infisical
```

`kubernetes-credentials` est un Secret ordinaire qui porte l'identifiant
de la machine :

```bash
kubectl -n infisical create secret generic kubernetes-credentials \
  --from-literal=identityId="$IDENTITY_ID"
```

Les deux objets doivent être `Ready` avant qu'un secret ne se synchronise :

```bash
kubectl -n infisical get infisicalconnection,infisicalauth
```

## La synchronisation

```yaml
apiVersion: secrets.infisical.com/v1beta1
kind: InfisicalStaticSecret
metadata:
  name: app-demo
  namespace: infisical
spec:
  infisicalAuthRef:
    name: contabo-vps
    namespace: infisical
  syncOptions:
    refreshInterval: 1m
  sources:
    - projectId: 22fe98db-b31b-4f2e-b412-8d1823a79a78
      environmentSlug: prod
      secretPath: /
  targets:
    - kind: Secret
      name: app-demo
      namespace: infisical
      creationPolicy: Owner
```

La colonne `SYNCED` passe à `True` quand la synchronisation a eu lieu.
Une source doit nommer **exactement** `projectId` ou `projectSlug` : les
deux, ou aucun, et la reconciliation échoue.

## La rotation

Un conteneur lit ses variables d'environnement une fois, au démarrage. Une
nouvelle valeur synchronisée dans le Secret Kubernetes ne change donc rien
pour un pod déjà lancé. L'operateur Infisical sait redémarrer les workloads
qui consomment un Secret synchronisé ; Kuberpack active ce comportement.

Quand un service de projet déclare `secrets`, le compilateur écrit dans ses
valeurs :

```yaml
deploymentAnnotations:
  secrets.infisical.com/auto-reload: "true"
```

Le chart `stateless` (0.1.8 et suivants) reporte cette annotation sur les
métadonnées du Deployment. À chaque synchronisation qui modifie réellement
le Secret, l'operateur liste les Deployments du namespace cible, retient
ceux qui portent l'annotation **et** consomment le Secret par `env`,
`envFrom` ou volume, puis pose l'etag du Secret sur le Deployment et sur son
template de pod. Le changement de template déclenche un rollout : les
nouveaux pods lisent les nouvelles valeurs. L'annotation n'est réécrite que
si l'etag change : une synchronisation sans modification ne redémarre rien.
L'implémentation est celle de l'operator épinglé ([auto-reload dans
`internal/services/infisicalstaticsecret`](https://github.com/Infisical/kubernetes-operator/blob/infisical-k8-operator/v0.11.13/internal/services/infisicalstaticsecret/reconciler.go)).
**Validation du 23 octobre :** l'annotation native seule ne suffisait pas
(l'operator mettait à jour le Secret sans redémarrer le Deployment,
`AFFECTED DEPLOYMENTS 0`). Kuberpack installe donc `kuberpack-reloader`, avec
son propre ServiceAccount et un RoleBinding par namespace de projet. Il lit
l'etag du Secret, pose `kuberpack.io/secret-revision` sur le template du
Deployment et déclenche le rollout. Rejeu Docker : pod remplacé et empreinte
servie égale à la nouvelle valeur.

L'annotation n'est posée que sur un service de projet qui déclare des clés.
Une preview, une application autonome ou un service sans secret ne la porte
pas : une rotation ne les redémarre jamais. Le control plane ne gagne aucun
droit et ne lit aucune valeur ; l'operateur reste le seul composant qui
parle à Infisical et le seul à écrire le Secret, dans les namespaces que son
scope GitOps lui donne déjà.

Une installation existante doit recevoir le chart à jour (`kuberpack
install`, qui recopie `charts/stateless` dans le dépôt GitOps) avant que
l'annotation ne soit rendue. Vérification :

```bash
kubectl -n <namespace-projet> get deploy <app> \
  -o jsonpath='{.metadata.annotations.secrets\.infisical\.com/auto-reload}'
kubectl -n <namespace-projet> rollout status deploy/<app>
```

Limites : la rotation automatique suppose que le processus relit ses
variables au démarrage, ce qui est le cas après le rollout. Renouveler un
mot de passe de base de données demande toujours d'agir des deux côtés
(Infisical et le serveur) ; l'isolation des projets/chemins Infisical reste
le risque distinct décrit dans [security.md](security.md).

## Le CLI

Le CLI d'Infisical parle à l'instance auto-hébergée comme à n'importe
quelle autre. C'est l'outil le plus direct pour écrire et lire un secret.

Un développeur utilise un **compte humain membre du projet**, avec un rôle
autorisant la lecture et l'écriture des secrets. Être administrateur de
l'organisation ne suffit pas à appartenir au projet. Un `403` indiquant
« not a member of this project » se corrige par ce rattachement.

```bash
infisical login --domain "$INFISICAL_DOMAIN"
infisical secrets set --file "$HOME/.config/kuberpack/stirling.env" \
  --domain "$INFISICAL_DOMAIN" --projectId "$INFISICAL_PROJECT_ID" --env prod
```

Ce parcours développeur ne demande aucun kubeconfig. Les exemples suivants
concernent l'automatisation par identité machine côté exploitation :

```bash
export INFISICAL_DOMAIN="https://infisical.exemple.org"
export INFISICAL_TOKEN="$(kubectl -n infisical get secret \
  infisical-bootstrap-secret -o jsonpath='{.data.token}' | base64 -d)"

# Ecrire un secret
infisical secrets set "MOT_DE_PASSE=une valeur" --projectId=... --env=prod

# Lire tous les secrets d'un environnement
infisical export --projectId=... --env=prod
```

Le CLI sait aussi s'authentifier comme l'opérateur, avec la méthode
Kubernetes :

```bash
kubectl -n infisical create token infisical-service-account --duration=10m > /tmp/sa.jwt
infisical login --method kubernetes \
  --machine-identity-id="$IDENTITY_ID" \
  --service-account-token-path=/tmp/sa.jwt \
  --domain="$INFISICAL_DOMAIN" --plain
```

## Ce que la plateforme attend

La plateforme écrit un `InfisicalStaticSecret` par application, dans
`kubernetes/vps/infisical/`, avec :

| Champ | Valeur |
| --- | --- |
| `infisicalAuthRef.name` | `KUBERPACK_INFISICAL_AUTH_REF`, `contabo-vps` par défaut |
| `infisicalAuthRef.namespace` | `KUBERPACK_INFISICAL_NAMESPACE`, `infisical` par défaut |
| `sources[].projectId` | `KUBERPACK_INFISICAL_PROJECT_ID` |
| `sources[].environmentSlug` | `secret_env` du service, `prod` par défaut |
| `targets[].name` | `app-<service>` dans le namespace du service |

L'`InfisicalAuth` référencé porte le nom `contabo-vps` et vit dans le
namespace `infisical`.

## Dépannage

**L'API ne répond plus et le cluster est figé.** Trop de pods, ou une
boucle de redémarrage. Regarder la charge du nœud avant de toucher à
quoi que ce soit : `top -bn1` sur le nœud montre si l'attente I/O est le
problème. Un pod en boucle d'OOM sature le disque, et le disque saturé
gèle l'API server.

**`middleware "X@kubernetes" does not exist`.** Le provider Kubernetes de
Traefik refuse les références à un middleware d'un autre namespace. Un
middleware déclaré dans le namespace `infisical` n'est pas trouvé par
Traefik : c'est le namespace de l'ingress qui compte, et le nom doit
porter le `@namespace` explicite.

**L'API renvoie 404 sur `/api/status`.** Le routage n'atteint pas le pod.
Vérifier les logs de Traefik : ils nomment le routeur et l'erreur.

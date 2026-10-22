# Infisical sur le cluster

Infisical est le coffre de secrets de la plateforme. Kuberpack ne lit
jamais une valeur de secret : quand une application déclare `secrets`, la
plateforme génère un objet `InfisicalStaticSecret` qui fait synchroniser
les valeurs depuis l'instance vers un Secret Kubernetes. Les valeurs ne
transitent ni par le dépôt Git, ni par la base SQLite, ni par le control
plane.

L'installation suit la [notice officielle](https://infisical.com/docs/self-hosting/deployment-options/kubernetes-helm).

## Ce qu'il faut

| | |
| --- | --- |
| Un cluster Kubernetes | 1.23 ou plus |
| Helm | 3.11.3 ou plus |
| Le dépôt des charts | `helm repo add infisical-helm-charts 'https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/'` |

Deux charts sont installés :

| Chart | Rôle |
| --- | --- |
| `infisical-standalone` | l'instance, avec son PostgreSQL et son Redis |
| `secrets-operator` | l'opérateur qui synchronise les secrets (CRD `InfisicalStaticSecret`) |

## Les valeurs

Les valeurs sont dans [`infisical-values.yaml`](infisical-values.yaml).
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

## L'installation

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

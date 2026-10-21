# Environnement de test Kuberpack

Un cluster Kubernetes réel, local, dans lequel Kuberpack peut être testé pour
de vrai : webhook → build Railpack/BuildKit → scan Trivy + SBOM Syft → digest
épinglé dans Git → réconciliation Flux → déploiement Helm.

Tout tourne dans **deux conteneurs Docker** construits ici sur une base
Debian, plus les images Kuberpack (control plane, builder, BuildKit) et
l'application de test.

```
┌─ hôte (Omarchy, btrfs) ──────────────────────────────────────────────┐
│                                                                       │
│  réseau docker kuberpack-test (172.29.90.0/24)                       │
│                                                                       │
│  ┌─ conteneur « forgejo » (172.29.90.10) ─────────────────────────┐  │
│  │  Debian 12 + Forgejo 17 (HTTPS, CA locale)                    │  │
│  │  · Git (API Gitea) : dépôt applicatif + dépôt GitOps           │  │
│  │  · registre OCI (/v2/) : images applicatives + cache BuildKit  │  │
│  │  · webhooks (HMAC) vers le control plane                        │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                       │
│  ┌─ conteneur « k3s » (172.29.90.11) ─────────────────────────────┐  │
│  │  Debian 12 + k3s v1.37.1 (privileged)                          │  │
│  │  · control plane + node unique                                 │  │
│  │  · namespace kuberpack      → control plane Kuberpack          │  │
│  │  · namespace kuberpack-build → Job de build (builder+buildkit) │  │
│  │  · namespace apps           → applications (HelmRelease)      │  │
│  │  · namespace flux-system    → source/kustomize/helm controllers│  │
│  │  · Traefik (ingress) sur 80/443                               │  │
│  └────────────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────────┘
```

## Adresses

| Service | URL / port | Remarque |
| --- | --- | --- |
| Forgejo (web + API + registre) | `https://172.29.90.10:3000` | certificat signé par la CA locale `testenv/pki/ca.crt` |
| Forgejo (SSH) | `ssh://kuberpack@172.29.90.10:22` | port 2222 publié sur l'hôte |
| API Kubernetes | `https://172.29.90.11:6443` | kubeconfig : `~/.kube/k3s-test.yaml` |
| Control plane Kuberpack | `http://172.29.90.11:30080` | NodePort ; API `Bearer <FORGEJO_CONTROL_TOKEN>` |
| Webhook Forgejo | `http://172.29.90.11:30080/hooks/forgejo` | HMAC-SHA256 |
| Ingress applicatif | `http://172.29.90.11` + en-tête `Host:` | ex. `hello-kuberpack.kuberpack.test` |

Le registre OCI est **le Forgejo lui-même** : l'hôte de l'image est dérivé de
`FORGEJO_URL`, comme l'exige le produit (`release.RegistryHost`).

## Prise en main

```bash
cd testenv

# état de l'environnement
docker ps
kubectl --kubeconfig ~/.kube/k3s-test.yaml get pods -A

# l'application de test
curl -H "Host: hello-kuberpack.kuberpack.test" http://172.29.90.11/
curl -H "Host: hello-kuberpack.kuberpack.test" http://172.29.90.11/healthz
```

Le chemin complet se rejoue avec un simple push :

```bash
# modifier l'application puis pousser
sed -i 's/0\.3\.0/0.4.0/' apps/hello-kuberpack/main.py
./scripts/push-app.sh "app: bump to 0.4.0"
```

Le webhook Forgejo déclenche le build, le digest est épinglé dans le dépôt
GitOps, Flux réconcilie et l'application est redéployée. Vérifier :

```bash
export KUBECONFIG=~/.kube/k3s-test.yaml
kubectl -n kuberpack logs deploy/kuberpack --tail=20 | grep -E "digest:|promoted"
kubectl -n apps get helmrelease
```

## Arborescence

```
testenv/
├── README.md
├── .env.test                  # adresses et jetons (mode 600, non versionné)
├── .forgejo-secrets.env       # SECRET_KEY / INTERNAL_TOKEN de Forgejo
├── pki/                       # CA locale + certificat du registre
│   ├── ca.crt, ca.key
│   ├── forgejo.crt, forgejo.key
│   └── ca-bundle-merged.crt   # CA de l'hôte + CA locale (images Kuberpack)
├── images/
│   ├── forgejo/               # Dockerfile + entrypoint (Debian + Forgejo 17)
│   ├── k3s/                   # Dockerfile + entrypoint (Debian + k3s)
│   └── kuberpack/             # surcouches : CA locale dans les images
├── apps/hello-kuberpack/      # application de test (Python + uv + FastAPI)
├── gitops/                    # contenu du dépôt GitOps (modèle local)
└── scripts/
    ├── bootstrap.sh           # recrée tout l'environnement
    ├── deploy-kuberpack.sh    # namespaces, secrets, chart kuberpack
    ├── push-app.sh            # pousse l'application de test sur Forgejo
    ├── capture-build.sh       # suit les logs d'un build
    └── smoke-test.sh          # vérifie la chaîne complète
```

## L'application de test

FastAPI en Python, verrouillée avec `uv` — c'est ce qui fait basculer la
stratégie `auto` sur `uv` (`pyproject.toml` + `uv.lock`), le build restant
assuré par Railpack.

| Fichier | Rôle |
| --- | --- |
| `pyproject.toml` | dépendances `fastapi` + `uvicorn[standard]` |
| `uv.lock` | lockfile vérifié par `uv lock --check` (stratégie `uv`) |
| `main.py` | `GET /` et `GET /healthz` (cible des probes et du test Helm) |

Le port est explicite, jamais supposé : l'enregistrement porte
`start_command: uvicorn main:app --host 0.0.0.0 --port $PORT` et `port: 8080`.

## Le dépôt GitOps

`kuberpack/gitops` sur Forgejo, branche `main` :

```
kubernetes/vps/apps/kustomization.yaml     # parent (kuberpack y ajoute les apps)
kubernetes/vps/apps/namespace.yaml         # namespace apps
kubernetes/vps/charts/stateless/            # copie du chart charts/stateless
kubernetes/vps/apps/<app>/values.yaml       # contrat runtime (image épinglée)
kubernetes/vps/apps/<app>/helmrelease.yaml  # HelmRelease (sourceRef = GitRepository)
kubernetes/vps/apps/<app>/kustomization.yaml
```

Flux lit `./kubernetes/vps/apps` ; le `HelmRelease` référence le chart dans le
même `GitRepository` (`infra-homelab`).

## Certificat et registre

Le registre OCI est servi en HTTPS par Forgejo avec un certificat signé par une
CA locale. Cette CA doit être dans le magasin de confiance de **tout** ce qui
parle au registre :

| Composant | Comment |
| --- | --- |
| control plane | image `kuberpack-test/control` (bundle CA remplacé) |
| builder (skopeo, git) | image `kuberpack-test/builder` |
| sidecar BuildKit | image `kuberpack-test/buildkit` (`KUBERPACK_BUILDKIT_IMAGE`) |
| containerd du nœud | bundle CA du conteneur k3s (image `kuberpack-test/k3s`) |
| source-controller Flux | ConfigMap `ca-bundle` + `SSL_CERT_FILE` (patch du Deployment) |

## Contraintes de la machine hôte

Trois contraintes de l'hôte ont été rencontrées ; elles ne concernent pas le
produit mais l'environnement de test.

1. **`btrfs` + `compress=zstd` : `fsync` à ~220 ko/s.** Le snapshotter
   `native` de BuildKit fait un `fsync` par fichier : une image de build
   prenait plus d'une heure. Le nœud k3s monte donc `/var/lib/kubelet` en
   **tmpfs** (20 Gio) : BuildKit passe alors sur le snapshotter `overlayfs` et
   l'extraction d'une couche passe de 110 s à 0,4 s.
2. **Mémoire.** Le conteneur builder était limité à 2 Gio : Trivy se faisait
   tuer par l'OOM killer en téléchargeant sa base de vulnérabilités. La limite
   est passée à 6 Gio (et le sidecar BuildKit à 8 Gio).
3. **`allowed_host_list` de Forgejo.** Le builtin correct est `private` (pas
   `private-network`) : sans cela Forgejo refuse la livraison du webhook vers
   une adresse RFC 1918.

## Recréer l'environnement

```bash
./scripts/bootstrap.sh
```

Le script recrée le réseau, les images, les deux conteneurs, la CA, les
dépôts, les jetons, Flux et le control plane. Il est idempotent : les volumes
(`kuberpack-forgejo-data`, `kuberpack-k3s-data`) et le dépôt GitOps sont
conservés.

# Kuberpack

PaaS GitOps visé : `git push` → image immuable → Flux.

Le CLI, le chart et le contrat sont ici. Ce qui est réellement implémenté :
[docs/status.md](docs/status.md). L'[audit du 22 octobre](docs/audit-2026-10-22.md)
détaille les parcours vérifiés et les défauts encore ouverts. Le modèle vise
un déploiement applicatif par Git et Flux ; l'implémentation actuelle écrit
aussi certains Secrets de registre et ConfigMaps applicatifs dans le cluster.

## Exigences

- cluster Kubernetes existant pour le control plane et les builds (test réel sur k3s) ;
- une forge Git : Forgejo/Gitea, GitHub ou GitLab ; registre OCI configurable séparément ;
- un dépôt GitOps lu par Flux sur le cluster de production ;
- builds rootless (BuildKit), images pinées par digest ;
- secrets hors Git, hors SQLite.

## Documentation

| Document | Contenu |
| --- | --- |
| [État du code](docs/status.md) | Implémenté vs contrat |
| [Vision](docs/vision.md) | Produit et principes |
| [Architecture](docs/architecture.md) | Plans, sources de vérité, k3s |
| [Flux](docs/flows.md) | Enregistrement, production, previews |
| [API](docs/api.md) | Contrat HTTP |
| [Données](docs/data-model.md) | SQLite opérationnel |
| [Builder](docs/builder.md) | Stratégies, tags, scans |
| [Isolation des builds](docs/build-isolation.md) | RuntimeClass, pool dédié, limites |
| [Stratégies de build](docs/strategies.md) | `auto` / `uv` / `railpack` : Railpack exécute toujours |
| [Sécurité](docs/security.md) | Tokens, webhooks, runtime |
| [Erreurs](docs/errors.md) | Comportement en échec |
| [Git Forgejo](docs/git.md) | Contrat API repris de l’écosystème Gitea |
| [Feuille de route](docs/roadmap.md) | MVP et phases |
| [Manifeste kuberpack.yaml](docs/manifest.md) | Format du manifeste compact projet |
| [Projets](docs/projects.md) | Architecture compilateur de projets |
| [Déploiement](docs/deployment.md) | Instance actuelle (homelab) |

## Install

Images : `ghcr.io/purrsec/kuberpack:latest` et `ghcr.io/purrsec/kuberpack-builder:latest`.

Avec le kubeconfig du cluster et les jetons de forge et registre dans
`KUBERPACK_GIT_TOKEN` et `KUBERPACK_REGISTRY_TOKEN` :

```bash
kuberpack install
```

Le questionnaire prépare Flux, les dépendances, Infisical et le control plane.
Le parcours reproductible et les prérequis sont dans [docs/install.md](docs/install.md).
La validation réelle couvre un [banc k3s Docker](docs/install-validation-2026-10-22.md)
et un [VPS vierge de 8 Go](docs/install-vps-2026-10-22.md) avec HTTPS public et Stirling PDF.
Vos applications complexes peuvent conserver leurs `helmfile.yaml` et charts
à côté du `kuberpack.yaml` : les releases restent gérées par leur outil choisi.

Après activation du profil CLI enregistré par l'installation :

```bash
kuberpack apps
kuberpack app status stirling-pdf-pdf
kuberpack app status --json stirling-pdf-pdf
```

Les états et réplicas proviennent du cluster via l'API ; le développeur
n'a pas besoin de kubeconfig. Voir les [états de déploiement](docs/api.md#consulter-les-déploiements).

## Code

CLI `kuberpack` : pin d’un digest dans le dépôt GitOps. Chart Helm `charts/stateless` : workload HTTP restreint.

```bash
make test
make build
./bin/kuberpack promote \
  --gitops-url https://git.host.bzh/pepe/infra-homelab.git \
  --gitops-branch main \
  --values-path kubernetes/vps/apps/hello-world/values.yaml \
  --chart charts/stateless \
  --image-repository git.host.bzh/pepe/hello-world \
  --commit-sha <sha> \
  --digest sha256:<digest>
```

```bash
make test
make build
export FORGEJO_TOKEN=<pat write:repository>
export KUBERPACK_API_TOKEN=$FORGEJO_TOKEN
./bin/kuberpack serve --addr :8080 --data data
# POST /api/v1/projects  Authorization: Bearer …
# POST /hooks/forgejo  HMAC X-Gitea-Signature
```

`POST /api/v1/projects` compile le manifeste `kuberpack.yaml` : il écrit le YAML GitOps (hostname, port, healthcheck), inscrit l’app, pose le webhook Forgejo et lance le premier build. Un `git push` sur la branche configurée construit, pin le digest et active Flux. Rien n’est commité dans le dépôt applicatif. Le [manifeste](docs/manifest.md) est la seule source de vérité.

`serve` est déployé sur le mini-pc (`https://kuberpack.host.bzh/`), hors du Flux `wait: true`.

## Installation

Depuis un profil CLI configuré, les déploiements et leurs logs se consultent
sans kubeconfig :

```bash
kuberpack apps
kuberpack app status stirling-pdf-pdf
kuberpack app logs stirling-pdf-pdf -f --tail 50
kuberpack app build-logs demo-web
```

`app logs` affiche l'exécution ; `app build-logs` affiche le builder.
Les images publiées n'ont pas de build Kuberpack.
`app status` inclut CPU/RAM, demandes et limites, redémarrages et bridage
CPU cumulé depuis le démarrage des conteneurs, avec les mesures indisponibles
signalées explicitement.

Voir [docs/install.md](docs/install.md) : un cluster, une forge git
(Forgejo, GitHub, GitLab), un registre OCI et deux jetons (un pour lire les
dépôts et écrire le GitOps, un pour publier et lire les images).

## État

Voir [docs/status.md](docs/status.md). En une ligne : webhook → Job Railpack → pin digest → Flux, avec previews PR, `rolled_back` via notification Flux, et archive de build optionnelle.

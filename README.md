# Kuberpack

PaaS GitOps pour Kubernetes : `git push` → image immuable → déploiement.

L’application n’embarque ni manifeste, ni pipeline, ni Dockerfile imposé. Elle s’enregistre une fois. Kuberpack construit le SHA reçu, publie un digest, et laisse Flux déployer.

Kuberpack s’installe sur **k3s**. Il n’applique pas les workloads de production : il n’a pas de kubeconfig du cluster applicatif. La production se décrit dans un dépôt GitOps et converge via Flux + Helm Controller.

## Exigences

- cluster k3s pour le control plane et les builds ;
- Forgejo (API Gitea) comme Git et, le cas échéant, registre OCI ;
- un dépôt GitOps lu par Flux sur le cluster de production ;
- builds rootless (BuildKit), images pinées par digest ;
- secrets hors Git, hors SQLite.

## Documentation

| Document | Contenu |
| --- | --- |
| [Vision](docs/vision.md) | Produit et principes |
| [Architecture](docs/architecture.md) | Plans, sources de vérité, k3s |
| [Flux](docs/flows.md) | Enregistrement, production, previews |
| [API](docs/api.md) | Contrat HTTP |
| [Données](docs/data-model.md) | SQLite opérationnel |
| [Builder](docs/builder.md) | Stratégies, tags, scans |
| [Stratégies de build](docs/strategies.md) | Analyse du code Kubero et contrat proposé |
| [Sécurité](docs/security.md) | Tokens, webhooks, runtime |
| [Erreurs](docs/errors.md) | Comportement en échec |
| [Git Forgejo](docs/git.md) | Contrat API repris de l’écosystème Gitea |
| [Feuille de route](docs/roadmap.md) | MVP et phases |
| [Déploiement](docs/deployment.md) | Instance actuelle (homelab) |

## Code

CLI `platform-deployer` : pin d’un digest dans le dépôt GitOps. Chart Helm `charts/stateless` : workload HTTP restreint.

```bash
make test
make build
./bin/platform-deployer promote \
  --gitops-url git@git.host.bzh:pepe/infra-homelab.git \
  --gitops-branch main \
  --values-path apps/web/values.yaml \
  --chart charts/stateless \
  --image-repository git.host.bzh/pepe/web \
  --commit-sha <sha> \
  --digest sha256:<digest>
```

```bash
make test
make build
export FORGEJO_TOKEN=<pat write:repository>
./bin/platform-deployer trigger --repository pepe/flask-uv --branch main
```

Le control plane HTTP n’est pas encore l’API d’enregistrement. `platform-deployer serve` expose `/healthz` pour un Deployment k3s. `trigger --strategy railpack` clone le SHA, lance `railpack prepare`, puis dispatche `app-release.yaml` sur le runner k3s. Promote reste une commande à part.

## État

Promotion Git + chart Helm + cobaye `hello-world` déployé par Flux sur le VPS (`hello-world.host.bzh`). Builder = workflow Forgejo existant. Pas encore de webhook ni de SQLite.

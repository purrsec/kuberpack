# Kuberpack

PaaS GitOps visé : `git push` → image immuable → Flux.

Le CLI, le chart et le contrat sont ici. Ce qui est réellement implémenté : [docs/status.md](docs/status.md). Kuberpack n’a pas de kubeconfig du cluster de production.

## Exigences

- cluster k3s pour le control plane et les builds ;
- Forgejo (API Gitea) comme Git et, le cas échéant, registre OCI ;
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
| [Stratégies de build](docs/strategies.md) | Analyse du code Kubero et contrat proposé |
| [Sécurité](docs/security.md) | Tokens, webhooks, runtime |
| [Erreurs](docs/errors.md) | Comportement en échec |
| [Git Forgejo](docs/git.md) | Contrat API repris de l’écosystème Gitea |
| [Feuille de route](docs/roadmap.md) | MVP et phases |
| [Déploiement](docs/deployment.md) | Instance actuelle (homelab) |

## Code

CLI `kuberpack` : pin d’un digest dans le dépôt GitOps. Chart Helm `charts/stateless` : workload HTTP restreint.

```bash
make test
make build
./bin/kuberpack promote \
  --gitops-url git@git.host.bzh:pepe/infra-homelab.git \
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
# POST /api/v1/apps  Authorization: Bearer …
# POST /hooks/forgejo  HMAC X-Gitea-Signature
```

`POST /api/v1/apps` écrit le YAML GitOps (hostname, port, healthcheck). Un `git push` sur la branche configurée construit, pin le digest et active Flux. Rien n’est commité dans le dépôt applicatif.

`serve` est déployé sur le mini-pc (`https://kuberpack.host.bzh/`), hors du Flux `wait: true`.

## État

Voir [docs/status.md](docs/status.md). En une ligne : hello-world et wattchman-website passent par Kuberpack (webhook → Job → GitOps → Flux). Le receiver cobaye est retiré.

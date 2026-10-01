# Sécurité

## Trois identités Git, jamais une

Un secret compromis ne doit pas ouvrir code + registre + GitOps.

| Identité | Droit | Interdit |
| --- | --- | --- |
| Control plane | Lire les dépôts apps, webhooks, clés de deploy read-only, statuts / commentaires PR | Push apps, push GitOps |
| Kuberpack (GitOps) | Écrire le dépôt GitOps (`main`, `previews`) | Dépôts apps, packages OCI, API Kubernetes |
| Builder | Push d’images ; optionnellement commit sur le dépôt d’archives de build (`KUBERPACK_BUILDS_GIT_URL`) | GitOps de prod, push apps |

Commits signés sur `main` si le dépôt l’exige : clé du bot, pas d’un admin humain.

Les bots de dépendances (Renovate, etc.) ne gèrent pas les images que Kuberpack promeut.

## Webhooks

- HMAC par application, hors SQLite ;
- signature sur le **body brut** (`X-Forgejo-Signature` / `X-Gitea-Signature`, SHA-256) — voir [git.md](git.md) ;
- taille de body limitée ;
- `push` et `pull_request` seulement ;
- déduplication par identifiant de livraison ;
- file asynchrone ;
- logs sans secret.

## Pull requests

Code non fiable : pas de secret de production, pas de token large, pas de socket Docker, pas de ServiceAccount privilégié. Isolation rootless. Forks refusés par défaut.

## Runtime de production

- non-root ; si l’image a un USER numérique, on le garde ; les images Railpack `USER root` passent à `65534` (chart) ;
- Pod Security `restricted` ;
- `allowPrivilegeEscalation: false` sauf le builder ;
- secrets via `envFrom` / `secretKeyRef`, pas de SDK de secrets dans l’app.

Le namespace de build est le seul à pouvoir être moins restreint (besoins BuildKit).

## Promotion Git

Rendu des manifestes avant push. Conflit sur `main` : rebase, revalider, retry seulement si le SHA est encore HEAD.

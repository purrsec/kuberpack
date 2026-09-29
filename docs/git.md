# Git (Forgejo)

Kuberpack parle l’API Gitea, donc Forgejo. Il n’est pas un client Kubero.

Le produit doit, sans fichier dans le dépôt applicatif :

1. lire le dépôt (`GET /repos/{owner}/{repo}`) ;
2. créer un hook JSON `push` + `pull_request` s’il n’existe pas ;
3. créer une clé de deploy SSH **read-only** ;
4. extraire le SHA (`after` sur push, `pull_request.head.sha` sur PR) ;
5. lister branches et pull requests.

## Identité de test

Un PAT utilisateur unique : `FORGEJO_TOKEN` avec `write:repository` (clone + dispatch du workflow `app-release.yaml`). `write:package` n’est pas requis sur ce jeton — c’est le runner k3s qui pousse l’image.

```text
FORGEJO_URL=https://git.host.bzh
FORGEJO_TOKEN=<pat>
```

Création : Forgejo → Paramètres → Applications → générer un jeton, case `write:repository`.

## HMAC

Vérifier HMAC-SHA256 du body **tel que reçu**. En-têtes : `X-Forgejo-Signature`, `X-Gitea-Signature`, éventuellement `X-Hub-Signature-256`. Hex, préfixe `sha256=` optionnel.

Ne pas re-sérialiser le JSON pour calculer la signature. Les implémentations qui font `JSON.stringify` avec indentation ne sont pas compatibles avec Forgejo.

## Inspiration, pas dépendance

Kubero a déjà enchaîné dépôt Gitea → clé read-only → webhook → reconstruction de l'application. Son rebuild automatique transmet la branche configurée au fetcher ; Kuberpack doit, lui, transmettre le SHA exact de l'événement jusqu'au builder.

On ne reprend pas : operator, CRD, UI, Nixpacks, Buildah, patch in-cluster de l’application, HMAC basé sur un JSON reformaté.

Nixpacks + build sur le cluster de production a été évalué et rejeté : le builder Kuberpack est rootless, sur k3s isolé, et la production ne bouge que par GitOps.

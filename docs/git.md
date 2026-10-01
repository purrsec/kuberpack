# Git (Forgejo)

Kuberpack parle l’API Gitea, donc Forgejo. Il n’est pas un client Kubero.

Le produit doit, sans fichier dans le dépôt applicatif :

1. lire le dépôt (`GET /repos/{owner}/{repo}`) ;
2. créer un hook JSON `push` + `pull_request` s’il n’existe pas ;
3. créer une clé de deploy SSH **read-only** (à l’enregistrement) ;
4. extraire le SHA (`after` sur push, `pull_request.head.sha` sur PR) ;
5. lister branches et pull requests ;
6. publier un commit status : contexte `kuberpack/production` (prod) ou `kuberpack/preview` (PR). `target_url` : hostname, commit d’échec, ou commit d’archive de build si configuré.

## Identités

Un PAT control plane (`FORGEJO_TOKEN`) pour API + GitOps. Des jetons builder dédiés (`kuberpack-builder-git-token`, `kuberpack-builder-registry-token`) dans Infisical, pas dans SQLite. La clone des apps se fait de préférence par la clé SSH read-only créée à l’enregistrement.

```text
FORGEJO_URL=https://git.host.bzh
FORGEJO_TOKEN=<pat write:repository>
KUBERPACK_BUILDS_GIT_URL=https://git.host.bzh/pepe/kuberpack-builds.git
```

Création : Forgejo → Paramètres → Applications → générer un jeton, case `write:repository`.

## HMAC

Vérifier HMAC-SHA256 du body **tel que reçu**. En-têtes : `X-Forgejo-Signature`, `X-Gitea-Signature`, éventuellement `X-Hub-Signature-256`. Hex, préfixe `sha256=` optionnel.

Ne pas re-sérialiser le JSON pour calculer la signature. Les implémentations qui font `JSON.stringify` avec indentation ne sont pas compatibles avec Forgejo.

## Inspiration, pas dépendance

Kubero a déjà enchaîné dépôt Gitea → clé read-only → webhook → reconstruction. Kuberpack n’est pas un client Kubero : SHA exact jusqu’au builder, HMAC sur le body brut, promotion Git par digest.

On ne reprend pas : operator, CRD, UI, Nixpacks, Buildah, patch in-cluster de l’application, HMAC basé sur un JSON reformaté.

Nixpacks + build sur le cluster de production a été évalué et rejeté : le builder Kuberpack est rootless, sur k3s isolé, et la production ne bouge que par GitOps.

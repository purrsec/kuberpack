# Builder

Kuberpack n’exécute pas le compilateur. Il lance un travail sur k3s et attend `{ image, tag, digest }`.

## Exigences

- tourne sur k3s, namespace isolé ;
- BuildKit **rootless** ; pas de daemon Docker privilégié ;
- clone du SHA exact ;
- push d’un tag `sha-<commit>` ; l’identité déployée reste le digest ;
- scan (Trivy CRITICAL, SBOM Syft) avant de rendre le digest ;
- pas de tag mutable utilisé comme identité de release ;
- échec du scan → pas de promotion Git ;
- quotas CPU, mémoire, durée.

Le mécanisme (Actions, `Job`, autre) n’est pas le produit. Le contrat l’est.

La séparation entre sélection de stratégie, recette et exécuteur est détaillée dans [Stratégies de build](strategies.md), à partir du code local de Kubero.

## Références d’image

```text
<registry>/<app>:sha-<commit>
<registry>/<app>:sha-<commit>@sha256:<digest>    # GitOps
<registry>/<app>:pr-<n>-<sha-court>@sha256:<digest>
```

Le tag documente le commit ; le digest identifie l'image. Un tag OCI n'est pas immuable par sa seule forme : le builder vérifie le digest après publication. La politique de collision reste à définir si le même commit est reconstruit avec une stratégie ou une configuration différente. Avant commit GitOps, le SHA doit encore être HEAD de la branche de production.

## Stratégies

| Entrée | Stratégie |
| --- | --- |
| `pyproject.toml` + `uv.lock` | `uv` figé, sans `.venv` dans l’image |
| Autre dépôt que Railpack sait préparer | Railpack + frontend BuildKit |
| Image déjà construite ailleurs | hors Kuberpack |

`uv` : Python depuis `.python-version` ou `requires-python` ; lockfile obligatoire et vérifié avec `uv lock --check` ou `--locked` ; processus non-root ; start/port dans l’enregistrement Kuberpack.

Nixpacks n’est pas une stratégie du produit.

Le builder ne pousse pas dans le dépôt applicatif, ne committe pas le GitOps, n’injecte pas de secrets de production dans un build de PR.

## Code actuel

Le code du builder k3s est dans `internal/buildexec` et `internal/builder`. `Dockerfile.builder` crée son image : Railpack, `buildctl`, Trivy, Syft, Skopeo et Git. Un Job contient le worker et un sidecar BuildKit rootless. Le worker clone le SHA demandé, prépare le plan Railpack avec la commande de démarrage enregistrée, construit une archive OCI, convertit cette archive en format Docker pour Trivy, vérifie qu’une cible a été analysée, génère une SBOM SPDX avec Syft, pousse `sha-<commit>` et vérifie le digest publié. Le control plane attend le statut du Job avant de lire le tag exact. Le Job porte un nom stable par livraison webhook pour qu’un redémarrage ne déclenche pas un second build.

Le builder se configure avec `KUBERPACK_BUILDER_IMAGE` sur `serve`. Il faut d’abord publier l’image puis installer `deploy/kuberpack-build.yaml`, affecter le ServiceAccount `kuberpack` au Deployment du control plane et autoriser son montage de token. L’image builder actuelle cible `linux/amd64`. Le Job garde ses logs pendant 24 h, puis est supprimé par TTL. La SBOM n’est pas encore publiée hors du Job.

Le webhook de `pepe/hello-world` a déjà déclenché un Job réel (`kuberpack-build-736ffeb3f6e1d4bb`) pour le SHA `f10cf296ea2c210d374847d1368d0ef9c848664c`. Le worker a publié `git.host.bzh/pepe/hello-world:sha-<commit>` au digest `sha256:127130f2edce05a906342bba4700c43b9718a6fb81891b6af62398308d92de02` ; `serve` a committé ce pin dans GitOps et Flux l’a déployé. Un Job antérieur lancé à la main (`kuberpack-build-7fc1ad70955cc0e2`) avait seulement prouvé le worker, pas le webhook. La publication des images Kuberpack vers Forgejo utilise Skopeo en format Docker v2 : le push direct de l’index OCI par Apple `container` a produit une référence que k3s ne pouvait pas tirer.

`railpack-release.sh` / `app-release.yaml` / `release-webhook.py` restent sur le homelab seulement pour `wattchman-website`. Kuberpack ne les appelle plus : `serve` exige `KUBERPACK_BUILDER_IMAGE` et lit uniquement le tag `sha-<commit>`. Le builder ne committe jamais le dépôt GitOps.

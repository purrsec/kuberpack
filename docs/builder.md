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

Un Job de test sur k3s a construit `pepe/hello-world` au SHA `d56dc0764424bcdf1d4d2e6de8bb9e61c3334fa4`, scanné l’image, généré sa SBOM et publié `git.host.bzh/pepe/kuberpack-smoke:sha-<commit>` au digest `sha256:468915261411d8d2b7ad7e34393393de7a25477878a7cb541a513fc45a4398f1`. La publication des images Kuberpack vers Forgejo utilise Skopeo en format Docker v2 : le push direct de l’index OCI par Apple `container` a produit une référence que k3s ne pouvait pas tirer.

`railpack-release.sh` / `app-release.yaml` restent disponibles pendant la migration.

`kuberpack trigger` utilise encore le workflow cobaye. `serve` utilise les Jobs si `KUBERPACK_BUILDER_IMAGE` est défini ; sinon il garde ce workflow. Le builder ne committe jamais le dépôt GitOps.

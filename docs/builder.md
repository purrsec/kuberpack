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

La séparation entre stratégie (`auto` / `uv` / `railpack`) et exécuteur (toujours Railpack) est dans [Stratégies de build](strategies.md).

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

`uv` : le lockfile est obligatoire et vérifié ; Railpack construit l’image. Start/port se configurent à l’enregistrement Kuberpack.

Nixpacks n’est pas une stratégie du produit.

Le builder ne pousse pas dans le dépôt applicatif, ne committe pas le GitOps, n’injecte pas de secrets de production dans un build de PR.

## Code actuel

Le code du builder k3s est dans `internal/buildexec` et `internal/builder`. `Dockerfile.builder` crée son image : Railpack, `buildctl`, Trivy, Syft, Skopeo et Git. Un Job contient le worker et un sidecar BuildKit rootless. Le worker clone le SHA demandé, prépare le plan Railpack avec la commande de démarrage enregistrée, construit une archive OCI, convertit cette archive en format Docker pour Trivy, vérifie qu’une cible a été analysée, génère une SBOM SPDX avec Syft, pousse `sha-<commit>` et vérifie le digest publié. Le control plane attend le statut du Job avant de lire le tag exact. Le Job porte un nom stable par livraison webhook pour qu’un redémarrage ne déclenche pas un second build.

Le builder se configure avec `KUBERPACK_BUILDER_IMAGE` sur `serve` (`ghcr.io/purrsec/kuberpack-builder:latest`). `helmfile apply` installe le namespace builder, le RBAC et le control plane. L’image cible `linux/amd64`. Le Job garde ses logs pendant 24 h, puis est supprimé par TTL. Si `KUBERPACK_BUILDS_GIT_URL` est défini, le worker pousse SBOM SPDX et un extrait de log dans ce dépôt (Flux ne le watch jamais). Sinon l’archive est un no-op.

`serve` exige `KUBERPACK_BUILDER_IMAGE` et lit uniquement le tag `sha-<commit>`. Le builder ne committe jamais le dépôt GitOps applicatif.

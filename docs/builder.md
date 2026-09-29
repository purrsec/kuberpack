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

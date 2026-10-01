# Stratégies de build

`strategy` à l’enregistrement : `auto`, `railpack` ou `uv`.

Railpack est **toujours** l’exécuteur. Le langage du dépôt n’ouvre pas un second builder.

| Entrée | Effet |
| --- | --- |
| `auto` | Si `pyproject.toml` + `uv.lock` : la stratégie affichée est `uv` (lock vérifié). Sinon `railpack`. Dans les deux cas le Job construit avec Railpack. |
| `uv` | Exige `pyproject.toml` et `uv.lock`. `uv lock --check` si `uv` est sur le PATH. Construction Railpack (le frontend consomme `uv.lock`). |
| `railpack` | Railpack sans exiger un lock Python. |

Nixpacks n’est pas une stratégie du produit. Aucun fichier de plateforme n’est exigé dans le dépôt applicatif.

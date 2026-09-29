# Déploiement actuel

Cette page décrit **une** installation. Ce n’est pas le contrat produit.

| Rôle produit | Instance |
| --- | --- |
| k3s control plane + builder | homelab, nœud mini-PC, namespace `ci-build` |
| k3s production | VPS, GitOps Flux |
| Forgejo / registre | `git.host.bzh` |
| Dépôt GitOps | `pepe/infra-homelab` |
| Secrets | Infisical |
| Ingress | Traefik, `*.host.bzh` |
| Builder existant | `app-release.yaml`, `railpack-release.sh` |
| Receiver à remplacer | `kubernetes/ci/release-webhook.py` |

Cobayes prévus : site stateless déjà servi par une image Railpack ; app Python `uv.lock` + `/healthz`.

Le builder historique tague `main-<run>-<sha>`. Les apps Kuberpack passent à `sha-<commit>` uniquement. Flux Image Automation et les commentaires `$imagepolicy` restent éteints. Renovate ne touche pas ces images.

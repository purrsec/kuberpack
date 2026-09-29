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

Cobaye actuel : `pepe/hello-world` (Flask, image Railpack) servi sur `hello-world.host.bzh` par Flux + chart `stateless`. Le builder historique tague `main-<run>-<sha>`. Les apps Kuberpack visent `sha-<commit>` ; ce n’est pas encore le tag poussé par `railpack-release.sh`. Flux Image Automation et les commentaires `$imagepolicy` restent éteints. Renovate ne touche pas ces images.

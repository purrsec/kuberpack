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
| Builder cobaye (à remplacer) | `app-release.yaml`, `railpack-release.sh` |
| Receiver à remplacer | `kubernetes/ci/release-webhook.py` |

Cobaye actuel : `pepe/hello-world` (Flask, image Railpack) servi sur `hello-world.host.bzh` par Flux + chart `stateless`. Le script homelab tague `main-<run>-<sha>` ; Kuberpack écrit déjà `sha-<commit>@digest` dans GitOps (pull par digest). Flux Image Automation et les commentaires `$imagepolicy` restent éteints. Renovate ne touche pas ces images.

## Image Kuberpack

Control plane : Chainguard `git` (Wolfi), plus binaires statiques (`kuberpack`, `railpack`, `buildctl`, `uv`). Pas de Debian, pas de `buildkitd`, pas de Docker privilégié. `skopeo` n’est pas dans l’image (le binaire ne l’appelle pas).

```bash
docker build -t git.host.bzh/pepe/kuberpack:dev .
docker run --rm -p 8080:8080 \
  -e FORGEJO_TOKEN \
  -e KUBERPACK_API_TOKEN \
  -e KUBERPACK_GITOPS_URL \
  git.host.bzh/pepe/kuberpack:dev
```

Ne pas coller Kuberpack dans le Kustomization mini-pc `wait: true`. Le déploiement GitOps est `kubernetes/kuberpack` réconcilié par Flux `kuberpack` (`wait: false`). Image locale `git.host.bzh/pepe/kuberpack:0.1.0` (`imagePullPolicy: Never`), ingress `https://kuberpack.host.bzh/`, webhook `https://kuberpack.host.bzh/hooks/forgejo`. API : `Authorization: Bearer` avec le token Infisical `app-builder-git-token`. Pas d’Authelia (HMAC + bearer).


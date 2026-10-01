# Déploiement actuel

Cette page décrit **une** installation. Ce n’est pas le contrat produit.

| Rôle produit | Instance |
| --- | --- |
| k3s control plane + builder | homelab, nœud mini-PC, namespaces `kuberpack` et `kuberpack-build` |
| k3s production | VPS, GitOps Flux |
| Forgejo / registre | `git.host.bzh` |
| Dépôt GitOps | `pepe/infra-homelab` |
| Secrets | Infisical |
| Ingress | Traefik, `*.host.bzh` |

Hello-world : Flask Railpack sur `hello-world.host.bzh`, pin `sha-<commit>@digest`, chart `stateless`. Wattchman : Vite/Caddy Railpack sur `test-stagging.host.bzh` (HelmRelease `apps/wattchman-website`) ; `wattchman.fr` reste Railway. Flux Image Automation et les commentaires `$imagepolicy` restent éteints. Renovate ne touche pas ces images.

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

Ne pas coller Kuberpack dans le Kustomization mini-pc `wait: true`. Le déploiement GitOps est `kubernetes/kuberpack` réconcilié par Flux `kuberpack` (`wait: false`). Pinner control plane et builder comme les apps : `sha-<commit>@sha256:…` (pas de tag `test-*` pour une nouvelle publication). Ingress `https://kuberpack.host.bzh/`, webhook `https://kuberpack.host.bzh/hooks/forgejo`. API : `Authorization: Bearer` (`KUBERPACK_API_TOKEN`). Pas d’Authelia (HMAC + bearer).

Tokens builder dédiés (Infisical → Secret `kuberpack-builder`) : `kuberpack-builder-git-token` (clone HTTPS de secours / archive git) et `kuberpack-builder-registry-token` (`write:package`). Ce ne sont pas le PAT du control plane. À l’enregistrement, Kuberpack pose une clé SSH read-only sur le dépôt app.

Archive de builds : `KUBERPACK_BUILDS_GIT_URL` (dépôt dédié, jamais une source Flux). Secret Flux vers Kuberpack : `KUBERPACK_FLUX_WEBHOOK_SECRET`. Previews : `KUBERPACK_PREVIEW_DOMAIN` (défaut `preview.host.bzh`).


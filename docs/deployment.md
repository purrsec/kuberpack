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

Image control plane : `ghcr.io/purrsec/kuberpack:latest` (plus `sha-<commit>`). Le GitOps pin le digest : `ghcr.io/purrsec/kuberpack:latest@sha256:…`. Renovate met à jour le digest quand `latest` bouge.

```bash
docker build -t ghcr.io/purrsec/kuberpack:dev .
docker run --rm -p 8080:8080 \
  -e FORGEJO_TOKEN \
  -e KUBERPACK_API_TOKEN \
  -e KUBERPACK_GITOPS_URL \
  ghcr.io/purrsec/kuberpack:dev
```

Une installation neuve se fait avec `helmfile apply` (chart `charts/kuberpack`). Ne pas coller Kuberpack dans le Kustomization mini-pc `wait: true`. L’instance homelab est `kubernetes/kuberpack` réconcilié par Flux `kuberpack` (`wait: false`). Ingress `https://kuberpack.host.bzh/`, webhook `https://kuberpack.host.bzh/hooks/forgejo`. API : `Authorization: Bearer` (`KUBERPACK_API_TOKEN`). Pas d’Authelia (HMAC + bearer). Images : `ghcr.io/purrsec/kuberpack` et `ghcr.io/purrsec/kuberpack-builder`, pin digest.

Tokens builder dédiés (Infisical → Secret `kuberpack-builder`) : `kuberpack-builder-git-token` (clone HTTPS de secours / archive git) et `kuberpack-builder-registry-token` (`write:package`). Ce ne sont pas le PAT du control plane. À l’enregistrement, Kuberpack pose une clé SSH read-only sur le dépôt app.

Archive de builds : `KUBERPACK_BUILDS_GIT_URL` (dépôt dédié, jamais une source Flux). Secret Flux vers Kuberpack : `KUBERPACK_FLUX_WEBHOOK_SECRET`. Previews : `KUBERPACK_PREVIEW_DOMAIN` (défaut `preview.host.bzh`).


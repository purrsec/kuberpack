# Installer Kuberpack

Kuberpack est une couche d'abstraction : il se pose sur un cluster Kubernetes
que vous avez déjà, et il prend en charge le chemin « push → build → image →
déploiement ».

Il n'a pas d'avis sur votre forge, sur votre registre d'images ni sur votre
cloud. Il lui faut un cluster, une forge git, deux jetons, et c'est tout.

## Ce qu'il faut

| | |
| --- | --- |
| Un cluster | EKS, GKE, AKS, un k3s, un kubeadm… n'importe lequel |
| Une forge git | Forgejo, Gitea, GitHub, GitLab |
| Un registre OCI | celui de la forge (Forgejo, GitLab) ou GHCR, ECR, GAR… |
| Deux jetons | un pour **lire** les dépôts, un pour **pousser** les images |

Les deux jetons sont séparés par principe : le jeton de build ne sert qu'à
pousser les images, celui de lecture n'a pas le droit d'écrire dans vos
dépôts d'application.

## Les deux jetons

| Jeton | Droit | Sert à |
| --- | --- | --- |
| **lecture** | `repo` (GitHub), `read_repository` + `api` (GitLab), `read:repository` (Forgejo) | cloner le code, lire les branches, poser les webhooks |
| **build** | `write:packages` (GitHub), `write_registry` (GitLab), `write:repository` (Forgejo) | pousser l'image dans le registre |

Le control plane ne reçoit que le jeton de lecture. Le jeton de build va au
builder, dans le Secret qui monte dans le Job de build :

```
/secrets/git-token       jeton de lecture   (clone du dépôt applicatif)
/secrets/registry-token  jeton de build     (push de l'image)
```

Les deux peuvent venir de fichiers ou d'Infisical (le builder lit aussi
`kuberpack-builder-git-token` et `kuberpack-builder-registry-token`).

## La configuration

```bash
# la forge
KUBERPACK_GIT_PROVIDER=github        # forgejo | github | gitlab
KUBERPACK_GIT_URL=https://api.github.com
KUBERPACK_GIT_TOKEN=<jeton de lecture>

# le registre d'images (par défaut : l'URL de la forge)
KUBERPACK_REGISTRY_URL=ghcr.io
```

Les anciennes variables `FORGEJO_URL` et `FORGEJO_TOKEN` restent reconnues :
rien ne casse.

| Forge | `KUBERPACK_GIT_URL` |
| --- | --- |
| Forgejo | `https://git.exemple.org` |
| GitHub | `https://api.github.com` (ou `https://ghe.exemple.org/api/v3`) |
| GitLab | `https://gitlab.com/api/v4` (ou `https://gitlab.exemple.org/api/v4`) |

## Le déploiement

```bash
helm install kuberpack ./charts/kuberpack \\
  --namespace kuberpack --create-namespace \\
  --set image=ghcr.io/kuberpack/control \\
  --set builderImage=ghcr.io/kuberpack/builder \\
  --set forgejoURL=https://api.github.com \\
  --set gitopsURL=https://github.com/vous/deploiement.git \\
  --set webhookURL=https://kuberpack.exemple.org/hooks/forgejo
```

Le chart est dans [`charts/kuberpack`](../charts/kuberpack). Il ne dépend
d'aucun service particulier : Flux s'installe à côté, et le `Kustomization`
qui pointe vers `kubernetes/vps/projects` est la seule ligne à ajouter à la
main.

## Ce que Kuberpack ne fait pas

- Il ne crée pas vos dépôts applicatifs.
- Il n'écrit aucun fichier dans vos dépôts applicatifs.
- Il ne gère pas vos clusters : il écrit des objets Kubernetes, c'est tout.

C'est ce qui permet de l'installer n'importe où : un cluster EKS avec GitHub
et GHCR, un GKE avec GitLab, un k3s à la maison avec Forgejo — le code est le
même, seuls les trois variables d'environnement changent.

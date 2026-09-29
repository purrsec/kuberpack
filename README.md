# Kuberpack

Control plane Railway-like pour le homelab : enregistrer une app Forgejo, construire
une image, promouvoir un digest dans GitOps.

Ce dépôt n'est pas un fork de Kubero. On y reprend uniquement ce qui a marché
(client Forgejo/Gitea : dépôt, clé de deploy en lecture seule, webhook HMAC,
SHA exact) et on branche notre builder (mini-PC, rootless) puis
`platform-deployer` → Flux.

Le dépôt d'infrastructure reste `infra-homelab`. Kuberpack n'écrit pas les
objets de production sur le VPS.

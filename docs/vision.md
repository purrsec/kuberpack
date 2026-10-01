# Vision

Kuberpack est un control plane PaaS. Le développeur pousse du code. La plateforme clone, construit, scanne, publie une image, puis **demande à Git** de changer le digest. Le cluster de production ne fait que converger.

L’expérience visée est celle de Railway ou Heroku. Le modèle d’exploitation visé est GitOps sur k3s, pas un orchestrateur parallèle.

## Produit

- un dépôt applicatif ne contient aucun manifeste Kubernetes, aucun workflow CI, aucun fichier de plateforme ;
- une application s’enregistre une fois (`POST /api/v1/apps`) ;
- un push sur la branche configurée construit le SHA exact ;
- l’artifact est une image OCI identifiée par digest ;
- un bot Git, Kuberpack, est le seul écrivain automatique de ce digest dans le dépôt GitOps ;
- Flux et Helm Controller déploient, testent, et rollback si la release n’est pas saine ;
- `autodeploy_pr: true` crée une preview ; fermer la PR la détruit.

## Principes

1. **Les dépôts applicatifs sont en lecture seule.** Kuberpack clone et publie des statuts. Il n’y commite rien.
2. **Git est la source de vérité de la production.** Kuberpack n’applique pas de Deployment sur le cluster applicatif.
3. **Les previews sont générées.** Branche Git jetable, `prune: true`, reconstructible.
4. **Le control plane n’exécute pas les apps.** Il orchestre. Un builder isolé construit. Flux déploie.
5. **SQLite = état opérationnel.** Secrets ailleurs (Secrets Kubernetes, gestionnaire de secrets).
6. **Le digest est l’identité.** Un tag n’est jamais la référence de production.
7. **k3s est la cible d’installation.** Control plane et jobs de build tournent sur k3s. Le cluster de production est un k3s (éventuellement un autre) géré par GitOps.

## Hors produit

- forker et maintenir un autre PaaS ;
- remplacer Flux, l’ingress ou le gestionnaire de secrets ;
- découvrir les digests par polling de tags mutables (Renovate, Flux Image Automation) ;
- construire avec Nixpacks/Buildah sur le cluster de production.

# Flux

## Enregistrement

```http
POST /api/v1/apps
```

Champs : [API](api.md).

Kuberpack vérifie le dépôt, l’inscrit, crée le secret HMAC (hors SQLite), pose le webhook Forgejo (`push`, `pull_request`), écrit le contrat GitOps (`values.yaml` avec `envFrom` vers `app-<name>`, HelmRelease, kustomization) **sans l’activer dans Flux**, puis lance le premier build. Le premier digest piné ajoute l’app au kustomization parent. Ensuite Kuberpack ne change plus que le champ `image`. Rien n’est écrit dans le dépôt applicatif. L’administrateur crée `app-<name>` (Infisical ou kubectl) avant ou après ; sans l’objet Secret, le pod de prod ne démarre pas.

## Production

```mermaid
sequenceDiagram
    participant Dev as Développeur
    participant F as Forgejo
    participant K as Kuberpack
    participant B as Builder
    participant R as Registry
    participant G as Dépôt GitOps
    participant X as Flux
    participant H as Helm Controller

    Dev->>F: push sur la branche de prod
    F->>K: webhook signé + SHA
    K->>K: HMAC, déduplication, file
    K->>B: build du SHA exact
    B->>B: stratégie, scan
    B->>R: tag sha-<commit>
    B-->>K: digest
    K->>F: commit status pending → success
    K->>K: SHA toujours HEAD
    K->>G: commit image pinée
    X->>G: reconcile
    X->>H: HelmRelease
    H->>H: upgrade, probes, test
```

- échec de build ou de scan → Git inchangé ;
- seul le HEAD courant de la branche peut être promu ;
- un job lent ne remplace pas un SHA plus récent ;
- un seul bot committe les digests applicatifs.

## Previews

Un drapeau : `autodeploy_pr: true`.

- release `<app>-pr-<numero>` ;
- hôte `pr-<numero>.<app>.<preview-domain>` ;
- quota, PSA `restricted`, ressources inférieures à la prod ;
- aucun secret de production ;
- forks ignorés sans approbation ;
- TTL + GC si le webhook `closed` est perdu.

Fermer la PR supprime le manifeste ; Flux prune.

## Webhook HTTP

HMAC faux → `401`. JSON invalide → `400`. Événement hors périmètre → `204`. Accepté → `202` immédiat, travail en file. Reprise au redémarrage depuis SQLite.

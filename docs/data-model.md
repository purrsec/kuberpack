# Modèle de données

SQLite en WAL. Une réplique k3s, `Recreate`, volume persistant, sauvegarde hors nœud.

Si Kuberpack s’arrête, les applications déjà déployées continuent. Seuls les nouveaux builds et promotions s’arrêtent.

## Schéma

```text
apps
├── id
├── name
├── forgejo_repository
├── production_branch
├── autodeploy
├── autodeploy_pr
├── configuration_json
├── webhook_id
└── created_at

webhook_deliveries
├── delivery_id (unique)
├── app_id
├── event_type
├── action
├── commit_sha
├── received_at
└── status

builds
├── id
├── app_id
├── environment
├── commit_sha
├── image_repository
├── image_tag
├── image_digest
├── infra_commit_sha
├── status
├── error
├── created_at
└── finished_at

previews
├── app_id
├── pull_request_number
├── head_sha
├── build_id
├── hostname
├── status
└── updated_at
```

## Invariants

- `delivery_id` unique ;
- un déploiement actif par couple application / environnement ;
- le dernier SHA gagne ;
- un artefact déjà en registre peut être réutilisé seulement si le commit, la stratégie, sa version et les paramètres de build correspondent, et si son digest est vérifié ;
- aucun secret dans SQLite.

Tokens, HMAC et clés SSH : Secrets Kubernetes (souvent alimentés par un gestionnaire de secrets).

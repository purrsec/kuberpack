# Erreurs

| Erreur | Comportement |
| --- | --- |
| Livraison webhook déjà vue | Ignorée |
| HMAC invalide | `401`, rien n’est enfilé |
| Repo ou branche hors catalogue | `204` |
| Build ou scan en échec | Statut Git en échec, GitOps inchangé |
| Registre indisponible | Retry, pas de commit |
| SHA n’est plus HEAD | Pas de promotion |
| Conflit sur le dépôt GitOps | Rebase, revalidation, retry si HEAD inchangé |
| Manifeste invalide | Pas de push |
| HelmRelease insaine | Rollback Helm, état `rolled_back` |
| Fermeture de PR manquée | Garbage collector |
| Redémarrage du control plane | Reprise depuis SQLite |

Un échec ne se présente jamais comme un déploiement réussi.

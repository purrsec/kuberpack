# Feuille de route

## MVP

Une app enregistrée sans toucher à son dépôt. Un push construit une image scannée. Le bot GitOps pin le digest. Flux déploie sur k3s. Un échec ne change pas la production. Helm rollback si la release n’est pas Ready. Une PR avec `autodeploy_pr` a une URL qui disparaît à la fermeture. Redémarrer le control plane ne duplique pas les builds. Aucun secret dans Git ou SQLite. Aucun Docker privilegié.

## Phases

1. **Promotion Git** — `kuberpack trigger` attend le digest et `promote` pin `sha-<commit>@sha256:…`. Le builder k3s est le chemin produit.
2. **Chart Helm stateless** — probes, test, `remediation.rollback`. Le stateful reste hors chart.
3. **Control plane** — `serve` : SQLite, HMAC, `POST /api/v1/apps`, file. Pas encore d’image k3s.
4. **Previews** — branche générée, `prune: true`, GC. Implémenté côté control plane.
5. **UX** — CLI ou UI : liste, redeploy ; logs UI hors `serve`.

## Plus tard

Jobs k3s à la place d’un workflow Git (déjà le chemin produit). Namespace par preview. Revert Git après rollback Helm. Progressive delivery. Pins `sha-<commit>` pour les images Kuberpack elles-mêmes.

## Décisions produit

| Sujet | Choix |
| --- | --- |
| Installation | k3s |
| Accès cluster de production | aucun kubeconfig |
| Writer de digest | bot Git, un seul |
| Tag mutable comme release | non |
| Builder | rootless sur k3s isolé |
| Langage du control plane | Go |
| HMAC | body brut |
| Upstream Kubero | contrat Git seulement |

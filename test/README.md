# Lancer les tests

Depuis la racine du dépôt, avec Docker et Python 3 :

```console
python3 test/run.py static
python3 test/run.py light
python3 test/run.py heavy
python3 test/run.py superheavy
```

Les quatre commandes exécutent les mêmes conteneurs et les mêmes contrôles
qu’en CI. Le niveau léger vise moins d’une minute, le lourd moins de cinq
minutes et le très lourd vingt à trente minutes. La préparation à froid est
mesurée à part et reste visible dans le temps total.

Les fixtures sont gérées par Ansible. Chaque run utilise des conteneurs,
volumes et réseaux propres, sans ports publiés ni kubeconfig du poste. Les
rapports JSON/JUnit et les diagnostics sont dans `.test-results/<run-id>/`.

Cette arborescence est l'unique banc de test du dépôt. L'ancien environnement
local partagé, ses images intermédiaires, ses certificats persistants et ses
exemples dupliqués ont été supprimés ; leur historique reste disponible dans
Git si une enquête sur une ancienne révision l'exige.

Les détails des scénarios, des budgets et de la qualification sont dans
[le guide des tests](../docs/testing.md). Les tests Go de fonctions privées
restent dans leur package ; cette arborescence organise les suites et leurs
environnements d’exécution.

## Arborescence

```text
test/
├── run.py          lanceur local et CI (static, light, heavy, superheavy)
├── measure.py      répétition d’une suite et statistiques (p50/p95)
├── common/         image d’outils, librairie Python, tâches et fixtures partagés
├── static/         contrôles statiques et contrats du harnais
├── unit/           suites Go + Ansible (unit-light sans playbook, unit-heavy et unit-superheavy avec playbook)
└── examples/       vérification manuelle Stirling, hors CI
```

`unit-light` ne contient qu’un `suite.json` : ce sont les tests Go hors
réseau, sans playbook Ansible. `measure.py` relance une suite plusieurs fois
(`--runs N`, défaut 10) pour mesurer les durées. `examples/` est manuel et
n’est jamais exécuté par `run.py` ni par la CI.

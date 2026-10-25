# Ancien banc de développement

Les tests et leur orchestration ont été déplacés dans [test/](../test/README.md).
Les scripts de bootstrap et de smoke test fondés sur des IP, noms et volumes
partagés ont été retirés. Leurs remplaçants utilisent des fixtures Docker
jetables gérées par Ansible, avec le même point d’entrée en local et en CI.

```console
python3 test/run.py light
python3 test/run.py heavy
python3 test/run.py superheavy
```

Les anciens modèles d’images, certificats publics et applications de ce dossier
restent disponibles pour consulter le banc historique. Ils ne sont pas utilisés
par les suites ni par GitHub. Les fichiers privés ignorés par Git et les
conteneurs déjà présents sur le poste ne sont ni importés ni supprimés par cette
migration. Le banc historique complet reste consultable à la révision
`da5fb9970ca8b3cf5f5894aa7835d4064376edbc`.

Le contrôle HTTP de Stirling est désormais fourni par une image Docker dédiée ;
voir [l’exemple Stirling](../examples/stirling-pdf/README.md).

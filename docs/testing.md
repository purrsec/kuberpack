# Tests locaux et GitHub

Le point d’entrée commun est `python3 test/run.py`. Les jobs GitHub exécutent
cette même commande dans le même conteneur d’outils. Docker et Python 3 sont
les seuls outils nécessaires sur le poste Linux amd64 ; Go, Ansible, Helm, kubectl,
Infisical et actionlint sont fournis par l’image de test.

## Commandes

```console
python3 test/run.py static
python3 test/run.py light
python3 test/run.py heavy
python3 test/run.py superheavy
```

Les alias Make sont `make test-static`, `make test`, `make test-heavy` et
`make test-superheavy`. `go test ./...` reste utilisable pour une boucle
locale ciblée, mais ne remplace pas ces suites.

| Suite | Budget des contrôles | Contenu |
| --- | ---: | --- |
| `static` | < 60 s | gofmt, go vet, actionlint, syntaxe Ansible, lint des charts, contrats du harnais |
| `unit-light` | < 60 s | Tous les tests Go sans accès extérieur, ordre aléatoire, exécution sans cache de résultat |
| `unit-heavy` | < 300 s | Détection des courses Go, admission CEL sur un vrai API server, RBAC et trafic entre workloads du chart réel |
| `unit-superheavy` | < 1 800 s ; cible 20–30 min | Opérateurs réels, installation, forge/registre HTTPS privés, build Railpack, scan Trivy, SBOM Syft, Flux, secrets, reloader, webhook et réinstallation |

Le budget est un délai effectif : une commande bloquée ou un dépassement fait
échouer la suite. La préparation initiale de l’image d’outils, des modules et
de la compilation est mesurée séparément. Un premier lancement à froid prend
donc plus longtemps qu’un lancement avec caches. Les rapports conservent le
temps des contrôles **et** le temps total de l’invocation ; aucun téléchargement
ou coût de compilation n’est présenté comme gratuit.

Mesures locales Docker sur la révision de code `492af22` : statique 4,9 s, léger
21,2 s, lourd 171,4 s et très lourd 1 038,9 s. Les durées totales avec
préparation sont respectivement 5,8 s, 22,2 s, 172,5 s et 1 040,3 s. Dix
passages légers consécutifs avaient auparavant donné une médiane de 22,1 s et
un p95 d'échantillon de 22,7 s. Ces mesures ne constituent pas un p95
représentatif des runners GitHub. Une révision n'est qualifiée que lorsque les
quatre niveaux passent également sur son SHA dans GitHub Actions.

## Arborescence

```text
test/
├── run.py                      lanceur local et CI (static, light, heavy, superheavy)
├── measure.py                  répétition d’une suite et statistiques (p50/p95)
├── common/                     image d’outils, librairie Python, tâches et fixtures partagés
│   ├── Dockerfile              versions et bases épinglées
│   ├── requirements.txt        dépendances Python épinglées
│   ├── collections.yaml        collections Ansible épinglées
│   ├── ansible.cfg             configuration Ansible du conteneur
│   ├── inventory.yaml          inventaire local
│   ├── images.json             images de fixtures épinglées par digest
│   ├── suite.py                délais, exécution et rapports
│   ├── privacy.py              collecte et expurgation des secrets
│   ├── check_privacy.py        garde-fou anti-fuite dans les artefacts
│   ├── diagnostics.py          diagnostics cluster et Docker
│   ├── finalize_report.py      rapport de secours en cas d’échec d’infra
│   ├── import_images.py        transfert producteur/consommateur vérifié
│   ├── fixtures/               charts et projets de test
│   ├── tasks/                  création, diagnostic et destruction
│   └── callback_plugins/       événements Ansible sans payload secret
├── static/                     contrôles statiques et tests du harnais
├── unit/
│   ├── unit-light/             tests Go hors réseau uniquement (suite.json seul, sans playbook)
│   ├── unit-heavy/             race detector + admission/RBAC/réseau (playbook.yaml + network.yaml)
│   └── unit-superheavy/        installation, build/scan réels, GitOps, secrets, reloader, réinstallation
└── examples/                   vérification manuelle Stirling, hors CI
```

Les noms des dossiers initiaux sont conservés. Les suites lourdes contiennent
aussi des tests d’intégration. Les fichiers `*_test.go` restent dans leur
package lorsque les tests utilisent des fonctions privées ; `test/` contient
la sélection, les fixtures et l’orchestration commune.

## Isolation et erreurs

Chaque exécution génère un identifiant `kpt-…`, son réseau Docker, ses volumes,
ses conteneurs et, pour le parcours complet, sa propre CA. Aucun port n’est
publié sur l’hôte. Les adresses sont attribuées par Docker et les noms
d’ingress sont résolus dans la fixture, sans dépendre de nip.io. Aucun
kubeconfig ou credential du poste n’est injecté.
Le nœud k3s utilise son propre espace de noms cgroup. Les niveaux lourds
nécessitent que Docker puisse démarrer un conteneur privilégié ; ce conteneur
reste une fixture jetable. BuildKit conserve son stockage standard.
Les fichiers de travail des pods vivent dans un volume Docker sur disque :
un build n’écrit pas ses couches d’images dans un `tmpfs` facturé à sa mémoire.

Les étapes critiques utilisent les modules Ansible de cycle de vie Docker,
de copie, de certificats et d’attente. Les assertions typées et la génération
des rapports utilisent Python. Aucun pipeline Bash n’orchestre les suites.
Le transfert d’images vérifie séparément le producteur Docker et le consommateur
containerd ; un échec d’une moitié du transfert est un échec du test.

Une réponse `Forbidden` ne suffit pas à prouver l’admission : le test vérifie
l’identité de la politique qui a refusé la requête. Les tests réseau vérifient
les IP et les listeners avant et après les refus. Seuls un rejet TCP ou un
timeout attendus comptent comme refus ; une erreur DNS, une commande absente
ou un échec de `kubectl exec` ne valident pas le contrôle.

Le test d’admission vérifie le marqueur du run dans le cluster avant toute
création. Les fixtures sont détruites dans les blocs `always` ; le lanceur
possède aussi un nettoyage final limité aux labels de son identifiant. Un
échec de nettoyage rend le run rouge, même si ses assertions étaient vertes.

Les dépendances publiques ne sont pas simulées dans le parcours complet.
Leurs téléchargements, la disponibilité des charts et le scan réel peuvent
faire échouer ce niveau. Ces contrôles sont séparés des tests hors réseau et
ne sont jamais transformés en succès ou en skip silencieux.

## Rapports et diagnostic

Par défaut, les artefacts vont dans `.test-results/<run-id>/` :

- `result.json` : résultats, attentes, observations, durées, révision et version Go ;
- `junit.xml` : assertions réussies, échouées et exclusions explicites ;
- `invocation.json` : identifiant et durée totale, préparation comprise ;
- événements Ansible et Go, sorties des commandes et diagnostics du cluster ;
- logs bornés des workloads et contrôleurs, révisions Flux et état des conteneurs.
  Les statuts des conteneurs conservent notamment les causes `OOMKilled` et
  les redémarrages, sans exporter leurs variables d’environnement.

`--artifacts <dossier>` choisit une destination neuve ou vide. Réutiliser un
ancien rapport est refusé. Les secrets de fixture restent dans le conteneur
et ses volumes temporaires ; les rapports n’exportent ni Secrets Kubernetes,
ni clés privées. Les logs sont expurgés des credentials connus et des JWT.
Une assertion finale recherche les valeurs sensibles et leurs encodages dans
les artefacts avant le nettoyage. Toute fuite détectée est expurgée et fait
échouer la suite ; une collecte incomplète des credentials fait aussi échouer
ce contrôle.

Les diagnostics précèdent la destruction. Une expiration ou un démarrage
impossible conserve également un rapport d’échec. GitHub téléverse les
artefacts avec `always()` ; la création de l’image d’outils qui échoue avant
le lancement des tests produit elle aussi un rapport explicite.

Quatre tests Go sont exclus du niveau léger par une liste nominative : trois
contrats nécessitant Internet et le test d’admission live. Les premiers sont
obligatoires dans le niveau très lourd, et l’admission dans le niveau lourd.
Un autre skip ou une commande verte sans événement de test fait échouer le
lanceur.

## CI

`.github/workflows/test-suite.yaml` contient l’unique recette de job Docker.
`.github/workflows/image.yaml` la réutilise : statique et léger, puis lourd,
puis très lourd sur les pushes, les tags, le nightly et le lancement manuel.
Les PR exécutent les trois premiers niveaux sans secret de dépôt. La
publication des images sur `main` ou sur un tag attend tous les niveaux.

Les caches de compilation Go sont vérifiés par Go à chaque utilisation ; le
cache des résultats de test est désactivé par `-count=1`. Les binaires exécutés
appartiennent à chaque conteneur, jamais à un répertoire partagé entre runs.

Pour utiliser un cache de fichiers comme GitHub :

```console
python3 test/run.py heavy --cache-dir .test-cache
```

Les autres exécutions utilisent un volume Docker de cache. `--skip-prepare`
permet de mesurer le coût d’une compilation dans le budget du niveau choisi.
L’option ne réduit ni les assertions ni leurs exigences.

## Périmètre de qualification

Le scénario très lourd installe cert-manager comme prérequis de sa CA locale,
puis utilise le véritable installateur pour Flux, les opérateurs manquants,
Infisical et Kuberpack. Cette préparation exerce la réutilisation d’un
cert-manager existant ; elle ne prétend pas valider une émission ACME publique.

Une application de test minimale utilise la bibliothèque standard Python pour
réduire les variations de dépendances applicatives. Son image est réellement
construite et scannée ; aucune image préconstruite ne remplace le build.
La configuration Railpack installe la version corrigée de `certifi` dans un
environnement sans `pip` et exclut les outils de construction du runtime.
Leur absence est également vérifiée par l’application réelle. Aucun résultat
de scan n’est ignoré pour la fixture. Le parcours vérifie aussi les révisions appliquées par Flux, la
reprise d’une installation interrompue sans rotation des secrets, la
préservation d’une application indépendante, le refus de destruction en
présence d’un PVC et l’absence de commit/build lors d’une réapplication identique.

La matrice des CNI cloud, l’upgrade entre deux releases publiées et les tests de
charge restent des extensions distinctes. Les premiers runs ne constituent
pas une qualification statistique : il faut conserver plusieurs passages
représentatifs et leurs durées avant de fixer un p95 ou une règle de branche.

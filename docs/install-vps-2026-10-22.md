# Installation sur VPS vierge — 22 octobre 2026

Le parcours fonctionne sur le VPS de test recréé avec **4 CPU et 8 Go de RAM**.
Après préparation de k3s, `kuberpack install` installe le socle, configure
Infisical et vérifie les accès HTTPS. Stirling PDF est ensuite déployé depuis
son seul `kuberpack.yaml`, après chargement des secrets par le CLI Infisical.

## Point de départ et périmètre

- VPS `scw-festive-banach`, `151.115.39.23`, Debian 13.7, amd64,
  disque de 32 Go, sans swap. L'utilisateur confirme que les anciennes
  données sont jetables et recrée lui-même la machine depuis Scaleway.
- À la première connexion, seul l'OS est présent. k3s **v1.36.5+k3s1**
  est préparé avec le script officiel, qui vérifie le téléchargement du
  binaire ; Traefik et local-path restent les composants standards.
  Cette préparation précède Kuberpack : `install` cible un Kubernetes existant.
- L'inventaire initial ne contient que les quatre namespaces standards,
  sans cert-manager ni Flux. Aucun import d'image dans containerd n'est fait.
- Le CLI tourne sur le poste opérateur, avec un kubeconfig administrateur
  privé. GitHub et GHCR utilisent le compte de test autorisé `user345679`.
- Racine dédiée : `clusters/vps-clean-20261022/kuberpack` dans le dépôt
  GitOps existant `user345679/deployement`. La racine historique DSH `./`
  n'est pas réconciliée sur le nouveau cluster.

## Résultats réels

| Vérification | Résultat |
| --- | --- |
| Installation initiale | Les sept étapes passent, sans réparation manuelle des ressources Kubernetes. |
| Dépendances absentes | Flux, cert-manager 1.21.2, CloudNativePG, Barman, Infisical et son opérateur sont installés. |
| Registre privé | Le control plane démarre depuis GHCR avec le Secret de pull créé par l'installateur. |
| HTTPS public | Les certificats de Kuberpack, d'Infisical et de Stirling sont émis par Let's Encrypt avec HTTP-01. La validation TLS reste active. |
| API publique | `/healthz` : 200 ; `/api/v1/apps` anonyme : 401 ; avec le jeton : 200. |
| Secrets développeur | Login humain et chargement dans Infisical avec le CLI, sans kubeconfig utilisable ni jeton de forge dans l'environnement développeur. |
| Déclaration Stirling | Compilation de `examples/stirling-pdf/kuberpack.yaml`, sans YAML Kubernetes ni chart applicatif écrit à la main. |
| Fonction PDF | Connexion : 200 ; fusion anonyme : 401 ; fusion authentifiée : 200, deux pages vérifiées avec `pdfinfo`. |
| Persistance | PDF téléversé, pod supprimé volontairement, puis fichier relu avec la même empreinte ; UID des deux PVC inchangés. |
| Compilation répétée | `changed: false`. |
| Réinstallation finale | Réussie sans commit supplémentaire ; empreintes de données et UID de 23 objets Secret/PVC conservés. |
| Réconciliation | Les deux Kustomizations et toutes les HelmReleases sont `Ready=True`. |
| Identifiants dans Git | Aucun identifiant connu en clair ou en base64 détecté dans le code et le périmètre GitOps de ce test. |

Adresses actives :

- API : `https://kuberpack.151.115.39.23.nip.io`
- Infisical : `https://infisical.151.115.39.23.nip.io`
- Stirling PDF : `https://pdf.151.115.39.23.nip.io`

## Défauts exposés par ce test et corrigés

Le premier déploiement neuf réussit. Les vérifications suivantes révèlent
deux défauts qui ne doivent pas être présentés comme des tests réussis :

1. Lors de la première réinstallation, les labels d'inventaire ajoutés par
   Flux sur le ClusterIssuer géré sont recopiés dans `platform/bootstrap.yaml`,
   produisant un commit inutile. Le rendu conserve maintenant uniquement le
   label de propriété de Kuberpack. Un test simule l'ajout de ces labels par
   Flux et vérifie que les fichiers générés restent identiques.
2. Lors d'une mise à jour d'image, une release encore saine peut représenter
   l'ancienne configuration. Le CLI annonçait le succès alors que le cluster
   utilisait toujours cette ancienne image. Il attend maintenant que les
   valeurs demandées soient présentes dans la HelmRelease, puis vérifie sa
   génération prête et l'API. Un test couvre une ancienne release saine.

Les corrections sont publiées dans les images privées de test, puis appliquées
avec `kuberpack install`. Une dernière réinstallation vérifie effectivement
l'absence de nouveau commit et la conservation des secrets et volumes.
Stirling reste accessible après ces mises à jour du control plane.

`make test`, `make build` et les tests de concurrence des packages CLI et
installation passent après les dernières corrections.

## Version finale et preuves

Révision GitOps finale : `6eaa6df27d0fbdcb65c832ba38a0cd5a0c5b9658`.
Charts : `kuberpack` 0.1.2 et `stateless` 0.1.6.

| Composant | Image privée épinglée |
| --- | --- |
| Control plane | `ghcr.io/user345679/kuberpack:install-d6075821465736c0@sha256:70de7f0d405038ae5520716af449af81c84df6a7124f01e1db84634d10cebb6c` |
| Builder configuré | `ghcr.io/user345679/kuberpack-builder:install-d6075821465736c0@sha256:049e334031eef9764b3812400d84cf4472f00e4f908efc90c65bc557da05e56c` |

Les sources corrigées restent locales, non commitées et non poussées. Les
tags `latest` publics ne constituent pas une publication de ces corrections.

Artefacts locaux :

- `/tmp/kuberpack-vps-final-result.json` : résultat agrégé et vérifications API.
- `/tmp/kuberpack-vps-reinstall-result.json` : réinstallation et empreintes.
- `/tmp/kuberpack-vps-stirling-result.json` : résultat fonctionnel et UID des PVC.
- `/tmp/kuberpack-vps-stirling-storage.json` : identifiant et empreinte du PDF.
- `/tmp/kuberpack-vps-before.json` : inventaire avant installation.
- `/tmp/kuberpack-vps-install.log` : installation initiale.
- `/tmp/kuberpack-vps-final-install.log` et `kuberpack-vps-final-reinstall.log` :
  application finale et réinstallation sans changement.

Les identifiants sont conservés hors du dépôt, en mode 0600 :

- CLI : `~/.config/kuberpack/clusters/vps-clean-20261022.env`.
- Admin Infisical : `~/.config/kuberpack/clusters/vps-clean-20261022-infisical.env`.
- Secrets Stirling : `~/.config/kuberpack/stirling.env`.
- Kubeconfig opérateur : `~/.kube/kuberpack-vps-clean.yaml`.
- Choix non secrets : `~/.config/kuberpack/install/vps-clean-20261022.yaml`.

Activation du CLI :

```bash
. ~/.config/kuberpack/clusters/vps-clean-20261022.env
./bin/kuberpack apps
```

Le profil humain du CLI Infisical se nomme `kuberpack-vps-clean-20261022` ;
le profil par défaut n'est pas remplacé.

## Mesures et limites

Après stabilisation, `kubectl top node` indique environ **4,7 à 5 Gio**
utilisés sur 7,8 Gio disponibles. Le disque utilise environ 12 Go, avec
19 Go encore disponibles. Le nœud ne signale pas de pression mémoire,
disque ou PID lors de la vérification finale. Ces mesures valident le socle
et Stirling dans ce scénario ; elles ne sont pas un benchmark de charge.

Les builds de code ne sont pas rejoués sur ce VPS de 8 Go. Leur validation
précédente, ainsi que la coexistence réelle avec un Helmfile indépendant,
figurent dans le [compte rendu Docker](install-validation-2026-10-22.md).
Cet essai ne certifie pas EKS/GKE, la HA, les restaurations ou la rotation
des secrets. Il complète l'[audit initial](audit-2026-10-22.md).

## Suite : observation des déploiements dans le CLI

L'observation est ajoutée ensuite et déployée sur le même VPS. Le CLI local
est accessible dans le PATH (`~/.local/bin/kuberpack` pointe sur le binaire
du workspace). Après activation du profil :

```bash
kuberpack apps
kuberpack app status stirling-pdf-pdf
kuberpack app status --json stirling-pdf-pdf
```

Stirling apparaît `running`, avec `READY 1/1`, `UPDATED 1/1` et `AVAILABLE 1/1`.
L'image et les compteurs sont lus dans Kubernetes par le control plane ;
le développeur du test utilise un kubeconfig inexistant et aucun jeton de forge.
L'endpoint de statut anonyme retourne 401, celui d'un nom inconnu 404.

Une application BusyBox temporaire, déclarée par un manifeste avec un
namespace différent de son projet, vérifie les transitions :
`running → degraded → running → stopped → running`. L'opérateur injecte
un `exit 17` dans ce workload jetable pour tester l'observation d'un défaut
réel, puis restaure sa commande. L'ancien réplica sain continue de servir
pendant le rollout en échec ; le statut affiche tout de même `degraded` et
la cause. L'arrêt et le redémarrage passent par `replicas` dans le manifeste.
Le projet temporaire et son namespace sont ensuite supprimés.

Les tests Go et de concurrence couvrent notamment les générations obsolètes,
les anciennes ReplicaSets en erreur, l'absence d'observation, les droits de
lecture et la migration de l'inventaire conservant les applications existantes.
Une nouvelle réinstallation ne produit aucun commit et conserve les 23
objets Secret/PVC contrôlés. Le test PDF et la vérification d'empreinte du
fichier conservé passent encore après la mise à jour.

Image du control plane mise à jour :
`ghcr.io/user345679/kuberpack:install-b1ed5f78cbe00436@sha256:b0f439da2595b2617ab7b7d906321b7e11a04eec243b813007427fc64af3c37f`.
Révision GitOps après nettoyage : `a6dea5126be39ae99b75babc580daa1ed408b316`.
Preuves : `/tmp/kuberpack-status-final-result.json` et
`/tmp/kuberpack-status-smoke.log`.
Le contrat et les limites des observations sont décrits dans
[l'API](api.md#consulter-les-déploiements).

## Suite : logs d'exécution

`app logs` lit désormais les logs d'exécution via l'API Kuberpack, y compris
pour Stirling qui utilise une image publiée et n'a donc pas de build local.
La lecture de cinq lignes de Stirling passe avec un kubeconfig développeur
inexistant et sans jeton de forge. Un suivi `-f --tail 0` reste connecté
pendant dix-huit secondes sans exiger de nouvelle sortie, puis Ctrl-C ferme
la commande proprement. Les requêtes anonymes retournent 401 et les noms
d'application inconnus 404.

Les logs de build restent accessibles avec `app build-logs`, ou avec
l'ancien `app logs --build N`. Le nouveau parcours, ses options et les
limites de rétention sont documentés dans [l'API](api.md#consulter-les-logs).

Les tests Go passent, avec vérification de concurrence. Ils couvrent le
filtrage par propriétaires, les conteneurs init, les options Kubernetes,
le suivi d'un réplica silencieux sans bloquer les autres, la fermeture
des connexions et la remontée d'une coupure kubelet après réception partielle.
La lecture utilise le SDK Go et n'exécute pas `kubectl` chez le développeur.

Un projet temporaire `runtime-logs-smoke`, dans le namespace distinct
`runtime-logs-test`, valide deux réplicas réels. Chaque conteneur écrit un
marqueur puis quitte une première fois avec le code 17 ; après redémarrage,
il sert HTTP et produit des lignes périodiques. Les vérifications passent :

- deux lignes par réplica avec `--tail 2`, et deux préfixes de pods distincts ;
- les deux marqueurs précédents retrouvés avec `--previous` ;
- sélection par durée et horodatages avec `--since 3s --timestamps` ;
- sortie vide réussie avec `--tail 0` et flux réel suivi jusqu'à Ctrl-C ;
- erreur explicite une fois `replicas: 0` appliqué depuis le manifeste ;
- suppression du projet et disparition de son namespace.

Les droits vérifiés pour le compte de service dans `stirling-pdf` sont
`get pods --subresource=log` autorisé, `get pods --subresource=exec` et
`create pods --subresource=exec`, ainsi que `get secrets`, refusés.
Stirling reste `running 1/1` ; sa connexion, sa fusion
PDF et le contrôle d'empreinte du fichier persistant passent encore.

Images privées de cette validation :

- Control plane : `ghcr.io/user345679/kuberpack:install-ed641d6a7f12bfea@sha256:8d3ae326a903092653ad8f6feeed8eff18cc8e04a28f2d8278a8d93113f064d9`.
- Builder configuré : `ghcr.io/user345679/kuberpack-builder:install-ed641d6a7f12bfea@sha256:dab9fdb0d08d909439793c2bfa191e9d6835cb42b807fe160cc96fee14561d35`.

La réinstallation finale ne produit aucun commit et conserve les 23 objets
Secret/PVC vérifiés. Le dry-run passe, toutes les réconciliations Flux sont
prêtes et le nœud ne signale aucune pression. Révision GitOps après
nettoyage : `5e28b75845da5025a56956f37e30b7ff8bd0da0e`.

Preuves : `/tmp/kuberpack-runtime-logs-final-result.json` et
`/tmp/kuberpack-runtime-logs-smoke.log`.

## Suite : ressources et bridage CPU dans le statut

`app status` et sa sortie JSON incluent maintenant les mesures CPU/RAM,
les demandes et limites, les pourcentages, les redémarrages et les compteurs
de bridage CFS. Ces derniers sont explicitement cumulés depuis le démarrage
des conteneurs actuels. La lecture se fait par les API de ressources et
kubelet avec TLS vérifié, sans commande exécutée dans les pods et sans
installation d'un service de monitoring supplémentaire.

Sur Stirling, la validation observe environ 29m CPU pour une limite de
1000m, 913 Mio de mémoire pour une limite de 4096 Mio, zéro redémarrage
et environ 6 % de périodes CPU historiquement bridées. La comparaison
d'un même échantillon avec l'API métrique Kubernetes confirme exactement
le CPU arrondi et la mémoire en octets. Les compteurs CFS correspondent à
l'identifiant du conteneur actuellement actif.

Un projet `resources-smoke`, dans le namespace distinct
`resources-smoke-runtime`, déploie deux BusyBox limités chacun à 50m CPU
et 64 Mio. Une boucle de calcul rend le bridage observable : plus de 90 %
des périodes sont bridées. Les demandes/limites et les usages agrégés sont
la somme des deux conteneurs ; le pourcentage de bridage est pondéré par
leurs périodes respectives. Les pods des contrôleurs et des tests Helm
ne sont pas inclus. L'arrêt par `replicas: 0` donne un état de ressources
`stopped` et des usages absents. Le projet et son namespace sont supprimés
après le test.

Les contrôles utilisent un kubeconfig développeur inexistant et aucun
jeton de forge. Les droits effectifs du control plane permettent `get nodes`,
`get nodes/metrics` et la liste des métriques de pods dans le projet.
`get nodes/proxy`, `get pods/exec` et `get secrets` dans le projet sont
refusés. Le chart du control plane passe à 0.1.3 ; celui des apps reste
stateless 0.1.6. Les certificats kubelet non fiables ne sont pas acceptés ;
une CA dédiée peut être configurée pour les autres distributions.

Les tests Go et de concurrence passent : métriques partielles ou périmées,
valeurs invalides, échantillons antérieurs au redémarrage, anciennes
instances CFS, refus d'accès et échecs TLS sont couverts. L'absence de
mesures conserve la disponibilité du déploiement et les limites connues.
Les tests PDF, authentification et persistance de Stirling passent encore.

Images privées déployées :

- Control plane : `ghcr.io/user345679/kuberpack:install-811986406080b4a7@sha256:12d62a0a5f7b5ebdf345ca832d5f75ceaecd2e16c4f861ba42dd1a6d96fe310e`.
- Builder configuré : `ghcr.io/user345679/kuberpack-builder:install-811986406080b4a7@sha256:9fa5e999832e91eef221a79a3bdfc364d76171a1ee23b2a14347dcf78be6f489`.

Preuves : `/tmp/kuberpack-resources-result.json`,
`/tmp/kuberpack-resources-source-comparison.json` et
`/tmp/kuberpack-resources-smoke.log`.
Le contrat et les limites sont décrits dans [l'API](api.md#mesures-de-ressources-et-bridage-cpu).

La réinstallation finale ne génère aucun commit et conserve les 23 objets
Secret/PVC vérifiés. Le dry-run et les réconciliations Flux passent, sans
pression signalée sur le nœud. Les logs d'exécution de Stirling fonctionnent
encore après la mutualisation de la sélection des pods. Révision GitOps
après nettoyage : `0200a6b4431357fbddaa934feafca7165344bc6f`.
Résultat agrégé : `/tmp/kuberpack-resources-final-result.json`.

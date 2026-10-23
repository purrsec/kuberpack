# Validation de l’installation — 22 octobre 2026

Ce compte rendu décrit le banc Docker et la préparation de l'essai suivant.
La suppression/recréation du VPS et les résultats sur la machine neuve de
8 Go sont documentés dans le [compte rendu VPS](install-vps-2026-10-22.md).

## Périmètre

La refonte remplace la commande partielle de l’audit initial par un parcours
complet en Go. Les bibliothèques Helm et Kubernetes existantes sont conservées ;
le port-forward utilise également le SDK Kubernetes. Aucun moteur Ansible
n’est ajouté au produit.

Le test utilise le réseau Docker de DSH, Forgejo et un k3s neuf. Le conteneur
DSH original est arrêté sous le nom `k3s-dsh-backup` ; son volume
`kuberpack-k3s-data` est conservé. Le VPS et les dépôts GitHub de l’audit
précédent ne sont pas réinstallés. Le dépôt local Forgejo `kuberpack/install-audit`
contient uniquement les déploiements de ce test.

## Corrections structurantes

- Étapes nommées, limitées dans le temps, annulables et rejouables en observant
  le cluster. Lease renouvelée pour empêcher deux installations concurrentes.
- Préflight des permissions, du stockage, de l’ingress et des collisions de
  responsabilité ; refus d’un dossier GitOps existant non réservé au produit.
- Charts embarqués dans le CLI. Flux 2.9.6 téléchargé avec son SHA-256 épinglé,
  ou Flux existant réutilisé sans mise à niveau implicite.
- Cert-manager et opérateurs présents réutilisés, y compris dans un autre
  namespace et sous un autre nom Helm. Les releases Helmfile restent externes.
- Secrets stables, mots de passe PostgreSQL/Redis générés, valeurs sensibles
  Helm synchronisées depuis un Secret plutôt que publiées dans Git.
- Infisical configuré par son API : projet, identité sans accès global avec
  lecture du projet, méthode Kubernetes et accès du compte humain initial.
- Namespace Helm défini pour le stockage des releases et pour le client de
  ressources. Les charts qui omettent `metadata.namespace` restent dans le
  namespace demandé.
- Reprise du bootstrap Infisical après une installation initiale interrompue,
  malgré le hook amont limité à `post-install`. Les objets d’authentification
  sont créés après le raccordement API ; connexion interne Kubernetes autorisée.
- Périmètres GitOps séparés pour le socle et les projets ; contrôles de santé
  des releases avant activation des projets. Les objets durables non secrets
  du bootstrap sont également confiés à Flux.
- Vérification de l’API interne, de son refus d’accès anonyme et des URLs
  publiques avec validation TLS. Profil CLI local protégé en mode 0600.
- StorageClass choisie propagée au control plane, aux volumes applicatifs,
  aux addons et au cache du builder. Jeton de registre séparé pour le pull.

## Rejouer le test

Le cluster Docker doit être vierge de Kuberpack ; les images de test doivent
être importées dans son containerd. Le script refuse une autre API que
`https://172.29.90.11:6443` et vérifie l’image du conteneur local.

```bash
make build
python3 testenv/scripts/install-smoke.py \
  --kubeconfig /tmp/kuberpack-install-kubeconfig.yaml \
  --cluster docker-clean \
  --control-image docker.io/kuberpack-test/control:install-audit \
  --helmfile /tmp/helmfile \
  --interrupt
```

Le script installe d’abord une application indépendante et cert-manager avec
un Helmfile. Il prépare un issuer signé par la CA du banc d’essai, interrompt
l’installation pendant Infisical puis la reprend. Il charge ensuite un secret
avec un compte humain, compile un seul manifeste applicatif et contrôle par
HTTPS l’empreinte de la valeur reçue. Le développeur de ce test utilise un
kubeconfig inexistant et aucun jeton de forge.

Les empreintes de secrets et UID de volumes sont comparés pendant la reprise
et la réinstallation. La seconde installation doit produire zéro commit GitOps.
Le Helmfile, les UID, données et spécifications de ses ressources sont comparés.
Le script conserve un `result.json` sans valeurs de secrets dans son dossier
temporaire ; le fichier privé de secrets y est protégé en mode 0600.

Le développeur de ce banc d'essai fait confiance à sa CA locale avec
`SSL_CERT_FILE=testenv/pki/ca-bundle-merged.crt`. Le test ne désactive pas
la validation TLS. Les certificats sont émis par le cert-manager extérieur
réutilisé, sans remplacer cet opérateur.

## Limites

- Cette validation réelle porte sur k3s ; EKS/GKE, leurs contrôleurs cloud,
  les clusters privés et les politiques restrictives demandent leur matrice
  de tests. Traefik et ingress-nginx ont une détection de namespace ; les
  autres dataplanes nécessitent une configuration explicite et compatible.
- Le test utilise une CA locale. Il vérifie l’émission et l’usage de certificats
  valides pour cette CA ; il ne teste pas un challenge ACME sur Internet.
- Les bases Infisical embarquées sont des instances simples. HA, restauration,
  stockage externe et rotation des identifiants ne sont pas couverts.
- La racine du socle utilise `prune: false` : une suppression d’un composant
  exige une opération explicite pour préserver données et ressources partagées.
- Les installations historiques de DSH nécessitent une migration de leurs
  responsabilités ; ce test n’affirme pas les adopter sans intervention.
- Les P1 de l’audit initial sur les builds orphelins et les écritures directes
  du control plane restent distincts de cette refonte de l’installation.

## Résultats

Scénario propre `docker-final`, exécuté avec interruption volontaire :

| Vérification | Résultat |
| --- | --- |
| Installation et reprise | Réussies sur un k3s neuf, sans réparation manuelle du cluster pendant ce scénario final. |
| HTTPS | Certificats de l’API, d’Infisical et de l’application émis par la CA de test ; validation TLS active. |
| Secrets et volumes | Empreintes des secrets et UID des PVC conservés pendant la reprise et la réinstallation. Le champ `volumeName`, rempli lors du binding normal, est exclu du contrôle d’immutabilité. |
| Accès humain | Login et chargement par le CLI Infisical réussis, sans kubeconfig développeur valide. |
| Application | Un `kuberpack.yaml` compilé ; l’empreinte servie par HTTPS correspond à la valeur chargée dans Infisical. |
| Compilation répétée | `changed: false`. |
| Installation répétée | Réussie, sans nouveau commit GitOps. |
| Coexistence Helmfile | Fichier, UID, données et spécifications des ressources externes inchangés. |
| Vérification Go | `make test` et tests de concurrence sur CLI, installation, control plane et store réussis. |

Résultat du script : `installation=ready`, `reinstall=passed`,
`interruption_recovery=true`, `human_secret_upload=passed`,
`application_secret_digest=matched`, `compile_idempotence=passed`,
`helmfile_coexistence=passed`.

Artefact local : `/tmp/kuberpack-install-smoke-dtnp29jk/result.json`.
GitOps après ce scénario : `b27ba18594ca668f28cd70cbda495c6ba8086b87`.
Image locale du control plane : `kuberpack-test/control:install-audit`,
ID `sha256:1f4f7a75412c5f2ab5cf22d4898c263d81a6bac7d18238d7d5bae2ce0dfa6125`.
Charts : `kuberpack` 0.1.2 et `stateless` 0.1.6.

Les premières itérations ont révélé les défauts de namespace Helm et de hook
Infisical décrits plus haut. Un contrôle du test confondait aussi le binding
normal d’un PVC avec son remplacement ; le scénario final a été rejoué sur
un nouveau cluster après correction. Ces essais préparatoires ne sont pas
présentés comme des installations réussies sans intervention.

## Vérification du builder après l'installation

Le premier build Go de contrôle a exposé un défaut de dimensionnement :
BuildKit était limité à 2 Gio et Kubernetes l'a marqué `OOMKilled` (137).
Le builder principal ne remontait que la déconnexion de `buildctl`.
Le défaut d'installation passe à 4 Gio pour BuildKit ; le diagnostic inspecte
aussi les sidecars natifs et remonte leur OOM et leur limite mémoire.
Un test couvre le cas du sidecar arrêté et celui du sidecar déjà redémarré.
La nouvelle configuration et l'image `kuberpack-test/control:install-final`
sont appliquées par `kuberpack install`, sans patch manuel de Job ou Deployment.

Résultat après correction : build Go n°2 `succeeded` en environ 161 secondes
(horodatages API), Trivy passé selon la politique existante et SBOM Syft générée.
Aucune exception de vulnérabilité n’a été ajoutée. Digest observé dans la
Deployment prête :
`sha256:db194b23250d7386ff3dee137fd05a2438b862715f4a376e1949883a45807be8`.
L’application `https://install-build-smoke.172.29.90.11.nip.io` répond HTTP 200
avec la validation TLS de la CA du banc d’essai.

Image finale locale : `kuberpack-test/control:install-final`,
ID `sha256:d9a68491cc22e7d88a218afbc4954f150d03e9773bfbb7f797eab87e6deab20f`.
Une nouvelle réinstallation après cette correction conserve les secrets,
les UID de PVC et les ressources Helmfile, sans changement GitOps ; le dry-run
passe également. Révision : `adbeb4793f702f5e4b8bc4a3e13b3bb9be00e878`.
Résultat détaillé : `/tmp/kuberpack-install-final-check-result.json`.

Le contrôle des identifiants connus, en clair et en base64, ne trouve aucune
correspondance dans 362 fichiers du code et du GitOps de test. L’ancien mot
de passe fixe du bootstrap Forgejo du banc d’essai a été retiré du script ;
les comptes existants n’ont pas été modifiés. `bash -n` passe pour ce script.

Le cluster local courant utilise le volume `kuberpack-install-final-data`.
Son kubeconfig est disponible dans `~/.kube/kuberpack-install-test.yaml` et
`~/.kube/k3s-test.yaml`. L’ancien kubeconfig DSH est conservé dans
`~/.kube/k3s-dsh-backup.yaml`. Le profil CLI courant est
`~/.config/kuberpack/clusters/docker-final.env` (mode 0600).

Les modifications du code restent locales, non commitées et non publiées.
Seuls les dépôts et images du banc d’essai ont été utilisés pour cette refonte.

## Premier contrôle avant un essai sur VPS vierge

Le 22 octobre, une vérification en lecture seule confirme que le VPS existant
est accessible et que ses applications fonctionnent toujours. Il n'a pas été
réinstallé. Une installation à partir du réseau, sans images préchargées,
reste à valider avant de présenter ce parcours comme reproductible sur un VPS neuf.

- Les deux images publiques `ghcr.io/purrsec/kuberpack:latest` et
  `ghcr.io/purrsec/kuberpack-builder:latest` sont téléchargeables anonymement.
  Leurs configurations ont été créées vers 13:03 UTC ; l'image finale du
  control plane testée localement date de 16:43 UTC. Ces artefacts publics ne
  constituent donc pas une publication des dernières corrections locales.
  Préparer des images correspondant au code testé, puis épingler leurs digests.
- Les images de l'audit dans le compte GitHub de test nécessitent une
  authentification. Leur accessibilité avec un jeton depuis le poste ne prouve
  pas leur disponibilité pour les premiers pulls d'un nœud neuf.
- Le chemin d'installation initiale de cert-manager doit être testé : le
  scénario Docker réutilisait celui d'un Helmfile. La version épinglée dans
  le produit, 1.18.2, est en fin de support ; sa matrice s'arrête à Kubernetes
  1.33, tandis que le VPS utilise 1.36.5. Voir les
  [versions prises en charge](https://cert-manager.io/docs/releases/).
  Mettre à jour cette dépendance et vérifier son installation initiale.
- Le challenge ACME public reste à exercer. Le DNS doit viser la nouvelle
  adresse et l'ingress être joignable sur 80/443. Le CLI peut être exécuté
  depuis le poste disposant de Git et du kubeconfig administrateur ; il n'a
  pas besoin d'être installé sur le VPS.
- Un essai d'installation neuve utilisera un périmètre GitOps dédié. Il ne
  réappliquera pas la racine historique `./` de DSH. Un périmètre déjà lié
  à un autre UID de cluster n'est pas automatiquement adopté.

Le VPS contient treize PVC liés, dont ceux d'Infisical, du control plane et
de Stirling PDF ; les données locales occupent environ 3,3 Gio. Leur inventaire
ne constitue pas une sauvegarde. La destruction du VPS supprimerait ces
données et les secrets locaux ; une remise en service des applications
existantes demande une sauvegarde et une restauration distinctes de `install`.

## Préparation du remplacement du VPS de test

L'utilisateur confirme ensuite que les données de ce VPS de test sont jetables
et choisit sa suppression/recréation. La recréation est effectuée depuis sa
console Scaleway ; aucun identifiant fournisseur n'est configuré sur le poste.
La même clé SSH de test sera utilisée. Aucun résultat d'installation sur ce
nouveau VPS n'est encore acquis à ce stade.

La préparation corrige deux points :

- cert-manager est désormais épinglé en **1.21.2**, avec `crds.enabled=true`.
  Le chart officiel est téléchargé et son rendu contient les six CRD attendues.
  L'opérateur externe du banc Docker n'est pas mis à jour.
- L'installateur crée aussi un Secret Docker dans le namespace du control
  plane et le référence dans ses `imagePullSecrets`, avant son premier
  démarrage. Le registre privé peut donc fournir les deux images de la
  plateforme sans import préalable dans containerd. Le test de régression
  contrôle les deux namespaces et l'utilisation du jeton du registre.

Deux images du code courant sont publiées dans le registre **privé** du
compte de test autorisé, sans modification des tags `latest` publics :

| Composant | Image et digest |
| --- | --- |
| Control plane | `ghcr.io/user345679/kuberpack:install-0bf8075d72d36eee@sha256:d2d3755471c871d64b66531e00c635ea15bdcfe9aab7cb65257c4dafb9ca57c1` |
| Builder | `ghcr.io/user345679/kuberpack-builder:install-0bf8075d72d36eee@sha256:0dd4b2ec4500e06845abb30004c6385aeda6691f991da86e848c50b0af8bc18d` |

`make test`, `make build`, les tests de concurrence des packages CLI et
installation et `git diff --check` passent après ces corrections. Le code
source reste local, non commité et non poussé. L'inventaire des artefacts et
l'empreinte du CLI sont enregistrés dans `/tmp/kuberpack-vps-artifacts.json`.

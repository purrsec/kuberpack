# Audit sécurité Kuberpack — 23 octobre 2026

## Verdict

La passe corrige des chemins d’injection, des fuites de credentials et des
permissions excessives. Le risque **CP compromis → écriture GitOps → Flux
privilégié** est désormais borné : le CP n’écrit plus le dépôt du socle, et
Flux applique chaque projet avec un ServiceAccount de tenant limité à son
namespace. Un CP compromis ne peut plus atteindre les droits du cluster ni les
Secrets de la plateforme.

Kuberpack est **strict par défaut** : un projet qui déclare des clés doit
fournir son propre `infisical.project_id`, et l’identité/le chemin sont dérivés
du projet. L’administrateur doit provisionner le projet et l’identité
Infisical. `--allow-shared-secrets` reste une échappatoire de développement.
Les jetons API peuvent être limités à un projet, mais le jeton global reste
administrateur. Les risques restants demandent des décisions d’exploitation ou
des clusters externes, pas seulement du code.

Le VPS a été arrêté par l’utilisateur. La validation de cette reprise utilise
uniquement le Docker local, Forgejo et k3s `v1.37.1+k3s1` à `172.29.90.11`.
Les changements précédents du workspace sont conservés ; aucun déploiement
GitHub/VPS n’est effectué. Le point de contrôle est commité à la fin de la
reprise, une fois les preuves rejouées.

## Corrections

| Surface | Défaut constaté | Correction |
| --- | --- | --- |
| Réseau services | Ingress autorisé à toutes les apps du namespace, peers sans port | Voisins dirigés par service, entrée générée sur la cible, sortie limitée au port déclaré et aux labels de projet/service |
| Réseau projets | Dossier assimilé à une frontière ; namespace partagé possible | Propriété unique du namespace entre manifestes, refus des namespaces réservés et migrations implicites, collisions d’identité globale refusées |
| Permissions implicites | Internet activé par défaut, isolation désactivable | Internet désactivé par défaut pour les projets ; `defaultDeny: false` refusé |
| Addons | Toutes les apps/addons du projet admis en entrée ; egress des addons/helpers ouvert | Consommateurs déclarés, réplication et dépendance `backup.s3` identifiés ; helpers de dump/bucket correctement étiquetés ; egress borné par moteur (DNS, API/opérateur, réplication, stockage S3) |
| Image CP | Shell et outils de build présents | Runtime scratch contenant Kuberpack, Git natif/HTTPS et ses bibliothèques ; hooks Git désactivés |
| Dispatcher builder | Création de Jobs dans un namespace Pod Security permissif | Admission API pour Jobs, Pods et `pods/ephemeralcontainers`, images/commandes/arguments et volumes bornés ; privilèges, jetons et accès hôte refusés |
| Credentials | Le PAT GitOps était réellement copié dans le Secret builder | Credential de source privé distinct, registre distinct, séparation bloquante par défaut, absence de repli implicite pour la clé API |
| Autorité GitOps | Le CP écrivait le chart et les Kustomizations qui pilotent Flux | Dépôt applicatif séparé, écrit par le CP ; chart et Flux restent dans le dépôt de la plateforme, possédé par l’installateur |
| Clones/registre | Credential de plateforme transmis à une origine tierce ou au registre | Clones publics anonymes, auth privée limitée à son origine, registre authentifié uniquement avec son credential dédié |
| Infisical | Manager ClusterRole avec Secrets et TokenRequests sur tout le cluster | Roles dans la liste exacte des namespaces gérés, scope GitOps mis à jour à la compilation/suppression ; web et données sans jeton monté |
| Entrées API | Concaténations YAML, symlinks, corps/logs insuffisamment bornés | Validation des scalaires/chemins, refus des symlinks, limites de lecture et de connexions ; observation liée aux UIDs réels des workloads |

Les changements de contrat sont documentés dans [security.md](security.md),
[manifest.md](manifest.md) et [install.md](install.md). Les manifests avancés
gérés par un Helmfile indépendant ne sont pas remplacés par cette passe.

## Preuves

| Vérification | Résultat |
| --- | --- |
| Tests Go de tous les packages | Passent |
| `go vet ./...` et `git diff --check` | Passent |
| Race detector : CLI, control, promote, release, fetch, buildexec, builder | Passe |
| Admission sur le vrai serveur API Docker | Jobs/Pods normaux acceptés ; 86 scénarios dangereux refusés, dont modification d’image et conteneur éphémère ; zéro warning CEL |
| Helm puis Kustomize sur le chart complet | Passe ; couvre le post-rendering utilisé par Flux |
| Image finale du CP | Seulement `git` et `kuberpack` dans `/usr/bin`, aucun shell, uv, Railpack ou buildctl ; Git fonctionne en rootfs read-only, capabilities retirées, no-new-privileges |
| Rendus des charts Infisical officiels épinglés | Scopes exacts et absence de jetons runtime vérifiés |
| Parcours installation, secrets humains, réinstallation et Helmfile | Passent sur le banc Docker : `installation=ready`, `reinstall=passed`, `human_secret_upload=passed`, `application_secret_digest=matched`, `compile_idempotence=passed`, `helmfile_coexistence=passed`. Après la correction des intervalles Flux, une réinstallation ciblée `--install-only` passe également. |
| Connexions réellement permises/refusées entre applications | Voisin déclaré, pod déclaré, DNS et listener auxiliaire autorisés ; port non déclaré, voisin non déclaré, sens inverse, autre service, autre projet et ingress sans egress source refusés. Infisical : web sans jeton, operator limité aux Secrets du projet, sans jeton Flux ni Secrets système. Les namespaces temporaires de ce test ont été supprimés. |
| `doctor network` sur le banc k3s `v1.37.1+k3s1` | Moteur kube-router/k3s détecté ; contrôle positif joignable, puis deny-all bloqué, verdict « appliquée » ; namespace temporaire supprimé. Aucun Secret lu. |
| Isolation des projets sur le banc Docker | `tenant_isolation=passed` : namespace possédé, ServiceAccount sans jeton, RoleBinding limité à `kuberpack-tenant`, Kustomization Flux par projet, identité autorisée dans son namespace et refusée dans `kube-system`. |
| Rotation réelle d’un secret Infisical | Pod remplacé par `kuberpack-reloader` après la rotation ; l’empreinte servie par HTTPS correspond à la nouvelle valeur. Le Secret seul ne suffisait pas (`AFFECTED DEPLOYMENTS 0`) : le reloader est requis. |
| Jetons API de projet sur le banc Docker | Création d’un jeton `project`, liste des apps filtrée à son projet, `404` sur une app d’un autre projet, révocation puis `401`. Le secret n’est affiché qu’à la création et seul son SHA-256 est stocké. |
| Périmètre Infisical par projet | Rendu de l’`InfisicalStaticSecret` avec le `project_id`, l’`auth_ref` et le `path` déclarés ; validations de chemin absolu et label DNS. En mode strict, l’identité et le chemin sont dérivés du projet. |
| Parcours installation en mode strict | `installation=ready`, `reinstall=passed`, `human_secret_upload=passed`, `application_secret_digest=matched`, `compile_idempotence=passed`, `helmfile_coexistence=passed`, `tenant_isolation=passed`. L’`InfisicalStaticSecret` porte `authRef kuberpack-<projet>` et `secretPath /projects/<projet>`. |

Le parcours d’installation a été rejoué avec un projet applicatif neuf
(`security-install-20261023`) : le secret chargé par le CLI Infisical est
retrouvé par HTTPS dans l’application, la compilation répétée renvoie
`changed: false`, la réinstallation ne produit aucun commit GitOps et les
ressources du Helmfile indépendant restent identiques. Le résultat complet est
écrit dans le `result.json` du dossier temporaire du script.

Les probes réseau sont exécutées entre pods réels, pas sur les seuls objets
NetworkPolicy : `wget` depuis un voisin, depuis un pod auxiliaire non étiqueté,
et `nslookup` pour le DNS. Un refus attendu doit être un échec réel du client,
pas seulement une règle présente dans l’API.

Commandes reproductibles :

```sh
go test ./...
go vet ./...
go test -race ./internal/control ./internal/promote ./internal/release \
  ./internal/fetch ./internal/buildexec ./internal/builder ./cmd/kuberpack
KUBERPACK_SECURITY_KUBECONFIG=$HOME/.kube/k3s-test.yaml \
  go test ./charts -run TestBuildAdmission -count=1
KUBERPACK_TEST_NETWORK=1 go test ./cmd/kuberpack \
  -run TestInfisicalChartsEnforceScopeAndRuntimeHardening -count=1
python3 testenv/scripts/install-smoke.py \
  --kubeconfig "$HOME/.kube/k3s-test.yaml" --cluster docker-final \
  --project security-install-replay-$(date +%s) \
  --control-image docker.io/kuberpack-test/control:security-audit-final \
  --helmfile /tmp/helmfile
python3 testenv/scripts/security-smoke.py \
  --kubeconfig "$HOME/.kube/k3s-test.yaml"
go run ./cmd/kuberpack doctor network --context default \
  --kubeconfig "$HOME/.kube/k3s-test.yaml" --yes
```

Le projet `security-install-20261023` est la fixture du dernier passage ; un
rejeu doit utiliser un nom neuf, comme le fait la commande ci-dessus.

Les scripts refusent une autre adresse de cluster. Les fixtures d’admission
sont supprimées à la fin, sans exécuter les images des cas dangereux. Le script
réseau crée puis supprime ses seuls projets temporaires. Les valeurs de secrets
sont capturées en mémoire et ne sont pas imprimées.

## Risques restants et ordre de travail

1. **Critique — Autorité GitOps du CP.** Séparer le dépôt et les credentials du
   socle de ceux des applications. Réconcilier les applications sous des SA
   limités à leurs namespaces. Le provisionnement de namespaces/RBAC doit être
   assuré par une identité distincte, avec un schéma étroit et un contrôle de
   propriété. Un simple `serviceAccountName` sur une Kustomization ne suffit pas
   si le CP peut modifier le dépôt du socle qui définit ce compte et ses droits.

   **Progression — dépôt séparé livré et validé.** `--gitops-apps-url` crée un
   dépôt applicatif que seul le control plane écrit ; le dépôt de la plateforme
   (chart, Flux, socle) reste possédé par l’installateur. Le chart `stateless`
   reste dans le dépôt de la plateforme et les HelmRelease le référencent par
   la source de la plateforme, donc le CP ne peut plus modifier le chart qu’il
   fait appliquer. Le contrôle a été rejoué sur le banc Docker avec deux dépôts :
   installation, réinstallation sans commit, secret humain, compilation
   idempotente et coexistence Helmfile passent.

   **Seconde moitié livrée et validée.** Le control plane provisionne, par
   projet, un namespace possédé (`kuberpack.io/project`), un ServiceAccount
   sans jeton dans le namespace de contrôle et une Kustomization Flux qui
   applique le projet avec cette identité. Le RoleBinding ne peut référencer
   que le ClusterRole `kuberpack-tenant` : le tenant reste dans son namespace.
   Des ValidatingAdmissionPolicy bornent ce que le CP peut créer (identité,
   role, schéma de Kustomization, namespace). La Kustomization globale
   historique est suspendue et non supprimée, pour ne pas pruner son inventaire.
   Le CP met à jour directement le scope de l’opérateur Infisical, puisqu’il
   n’y a plus de Kustomization globale pour porter ce ConfigMap. Le banc Docker
   valide le parcours complet, l’identité limitée à son namespace, la
   réinstallation sans commit et la coexistence Helmfile (`tenant_isolation:
   passed`). Le layout à un seul dépôt n’est plus accepté à l’installation :
   une installation historique doit créer le dépôt applicatif, y déplacer ses
   projets et relancer `kuberpack install`. L’ancienne Kustomization globale est
   suspendue avant la bascule. Les credentials du CP restent suffisants pour
   écrire le dépôt applicatif et pour créer les objets de tenant (bornés par
   l’admission), pas pour le socle.

2. **Élevé — Secrets et autorisation par projet.** La clé API est administrateur
   global, l’identité/operator Infisical est partagé, et les manifestes utilisent
   le même projet/chemin de secrets. Séparer les identités, permissions et chemins
   avec une migration explicite des clés existantes. Un préfixe de chemin seul
   n’est pas une restriction si l’identité peut encore lire les autres chemins.

   **Progression — jetons API de projet livrés.** `POST/GET/DELETE
   /api/v1/tokens` administrent des jetons stockés en SQLite : seul le SHA-256
   du secret `kp_<id>_<secret>` est conservé, le secret n’est affiché qu’à la
   création, la comparaison est en temps constant et la révocation/expiration
   est immédiate. Un jeton de projet ne voit que ses apps, builds, logs et
   projets, ne peut compiler ou supprimer que son projet, et les routes de
   gestion des jetons restent réservées à l’administrateur ;
   `KUBERPACK_API_TOKEN` continue de fonctionner comme avant. **Limites** :
   l’identité et le projet/chemin Infisical restent partagés, les webhooks HMAC
   par application ne sont pas concernés, et il n’existe ni quota ni anti-rejeu
   par jeton. Un jeton administrateur créé par l’API équivaut au jeton global.
   Un jeton de projet peut réécrire le manifeste de son projet (namespace
   compris) et choisir les dépôts, images ou chemins Infisical qu’il référence :
   il borne l’API, pas un auteur de manifeste hostile.

   **Progression — périmètre Infisical par projet disponible.** Un manifeste
   peut déclarer `infisical.project_id`, `infisical.auth_ref` et
   `infisical.path`; le compilateur génère alors l’`InfisicalStaticSecret` avec
   ces valeurs au lieu du projet/chemin/identité partagés. L’administrateur
   crée le projet et l’identité Infisical et accorde l’opérateur ; Kuberpack ne
   détient aucun jeton d’administration Infisical. Cette frontière est le
   **défaut** : `infisical.project_id` est requis dès qu’un service déclare des
   clés, `auth_ref` et `path` sont dérivés du projet, et une référence à
   l’identité ou au chemin d’un autre projet est refusée. L’administrateur doit
   créer le projet et l’identité Infisical ; un CP compromis peut encore
   référencer une autre `auth_ref` déjà existante, donc l’administrateur ne doit
   pas laisser d’identité plus large disponible. `--allow-shared-secrets` est
   une échappatoire de développement qui réautorise le projet partagé ; elle
   n’est pas destinée à la production.

3. **Élevé — Ownership des namespaces externes.** Le compilateur empêche le
   partage entre projets Kuberpack et protège les namespaces connus/configurés.
   Il ne prouve pas qu’un namespace arbitraire préexistant est libre ou qu’aucun
   SA externe privilégié n’y réside. Le provisionneur doit refuser l’adoption
   implicite de tels namespaces avant d’y étendre les droits Infisical.

   **Corrigé côté compilation.** Le namespace généré porte
   `kuberpack.io/project=<projet>`. Avant toute écriture, l’API et la
   recompilation par webhook lisent le namespace réel : un namespace qui
   appartient à un autre projet, à un autre propriétaire ou à un usage inconnu
   est refusé. Une adoption volontaire se fait par l’annotation
   `kuberpack.io/adopt=<projet>` ; le layout historique déjà possédé par Flux
   reste accepté pour la migration. Un acteur qui peut déjà modifier les labels
   Flux ou créer des namespaces a déjà des droits de cluster. Le provisionneur
   de tenant crée désormais les namespaces et leurs identities ; son pouvoir
   est borné par l’admission décrite au risque 1.

4. **Élevé — Sandbox des builds.** BuildKit garde des exceptions rootless,
   `--oci-worker-no-process-sandbox` et le partage d’un noyau avec le cluster.
   L’admission réduit les possibilités du dispatcher ; elle ne protège pas
   contre une faille du noyau ou de BuildKit. Un chemin opt-in (RuntimeClass
   gVisor/Kata, pool de nœuds dédié, champs épinglés par l’admission) est
   documenté dans [Isoler les builds](build-isolation.md) ; il n’est pas actif
   par défaut et ne remplace pas cette limite. Les tags d’images doivent être
   épinglés en production et les scopes effectifs des credentials vérifiés.

5. **Moyen — Réseau des addons/helpers.** Le refus d’entrée du namespace et les
   policies des services sont actifs ; les pods des addons et leurs helpers
   (`bootstrap`, `dump`, `bucket`) déclarent maintenant un egress borné par
   moteur : DNS, API/opérateur, réplication et stockage `backup.s3`. Les limites
   restantes sont celles du modèle natif : pas de filtrage par nom de domaine
   pour un S3 externe, et couverture de l’API dépendante du CNI quand elle est
   hors du réseau des pods. L’option private-build-egress désactive également
   une protection : préférer ultérieurement une liste privée explicite.

6. **Moyen — Moteur réseau et limites des policies natives.** La présence d’un
   objet NetworkPolicy ne prouve pas son application. Valider EKS/GKE avec leurs
   modes CNI et leur comportement initial. Les policies sont additives ; un autre
   workflow peut rouvrir le réseau. Internet autorisé permet d’atteindre une
   application publique d’un autre projet ; nœud et localhost gardent des
   exceptions au modèle natif.

   **Progression — détection et probe livrées.** `kuberpack install` détecte
   désormais le moteur réseau (k3s/kube-router, Calico, Cilium/GKE Dataplane V2,
   AWS VPC CNI, Azure NPM) et avertit si l'application n'est pas établie.
   `kuberpack doctor network --context <contexte> --yes` observe un refus réel
   pod à pod dans un namespace temporaire, puis nettoie tout ; il refuse le
   contexte implicite, exige `--yes` et ne lit aucun Secret. La
   [matrice réseau](network-matrix.md) documente EKS VPC CNI standard/strict,
   GKE Dataplane V2/Calico, k3s et les policies additives. La commande est
   couverte par des tests unitaires avec fake client et a été rejouée sur le
   banc Docker k3s `v1.37.1+k3s1` le 23 octobre 2026 (joignable avant deny-all,
   bloqué après, namespace supprimé) ; EKS, GKE et AKS restent à valider sur
   leurs clusters réels. Le mode standard d'EKS continue de n'isoler que les
   pods sélectionnés, et une policy additive peut encore rouvrir le réseau.

7. **Moyen — Rotation des secrets runtime.** Le replay initial sur
   l’application de test avait montré qu’une nouvelle valeur synchronisée ne
   redémarre pas le pod qui la reçoit par variable d’environnement.

   **Corrigé et validé sur le banc.** L’annotation native
   `secrets.infisical.com/auto-reload` seule ne suffisait pas : l’operator
   mettait à jour le Secret sans redémarrer le Deployment (`AFFECTED
   DEPLOYMENTS 0`). Un composant dédié, `kuberpack-reloader`, tourne désormais
   avec son propre ServiceAccount ; le provisionneur lui donne un RoleBinding
   dans chaque namespace de projet, limité aux Secrets et Deployments de ce
   namespace. Il compare l’etag `secrets.infisical.com/version` du Secret
   consommé et pose `kuberpack.io/secret-revision` sur le template du
   Deployment, ce qui déclenche le rollout. Le control plane ne lit aucun
   Secret. Rejeu Docker : rotation réelle, pod remplacé, empreinte servie par
   HTTPS égale à la nouvelle valeur. Limites : un redémarrage initial au
   premier passage, pas de reloader en mode mono-dépôt historique, volume
   persistant en `Recreate` (brève indisponibilité), mot de passe de base de
   données toujours manuel des deux côtés. Le projet/chemin Infisical partagé
   reste le risque 2.

8. **Moyen — Résilience API/SSRF.** Les limites ajoutées ne constituent pas une
   politique complète de quotas par utilisateur, d’anti-rejeu ou d’egress du CP.
   Une référence de registre arbitraire mérite une revue SSRF et une politique
   d’origines/réseau. Aucun buffer overflow n’a été démontré ; les bibliothèques
   natives et images tierces n’ont pas fait l’objet d’un audit mémoire exhaustif.
   Suivi : la passe [Durcissement des accès sortants](security-egress-2026-10-23.md)
   ajoute une garde SSRF (IP dialée, redirections, allowlist), des limites par
   IP et une NetworkPolicy d’egress optionnelle du control plane. Il reste un
   quota par identité, l’anti-rejeu et l’activation de l’egress réseau.

## Déroulement et limites de cette validation

Un premier essai Docker a embarqué une version intermédiaire du YAML d’admission
pendant son édition parallèle et a échoué au post-rendering Helm. Le correctif
est couvert par un test Kustomize complet. L’image finale embarque exactement le
template validé ; un redémarrage du kustomize-controller du bac à sable a libéré
l’attente de santé de cette ancienne révision. Cela n’est pas présenté comme une
mise à jour réussie sans intervention dès le premier essai.

La dernière vérification a aussi corrigé une course entre les deux réconciliations
Flux : le socle et les projets partageaient le même intervalle de 30 s, et la
fenêtre de readiness du socle faisait échouer la vérification des projets. Le
socle passe à 5 min (les événements de source le réconcilient toujours
immédiatement) et les projets restent à 30 s. Le parcours complet avait déjà
passé ; une réinstallation ciblée `--install-only` valide la nouvelle cadence
sans rejouer les secrets applicatifs.

La passe ne modifie pas les secrets existants vers un autre chemin Infisical,
ne déplace pas les PVC et n’invente pas de garantie multi-tenant. Deux policies
d’admission temporaires avaient été créées sur le VPS avant son arrêt lors de
la session précédente ; elles devront être inventoriées/nettoyées avant une
future mise à jour de cette ancienne instance pour éviter une collision de
propriété Helm. Aucun accès VPS n’a été nécessaire pendant cette reprise.

## Sources et revues détaillées

- [Revue API et entrées](security-api-2026-10-23.md).
- [Revue des pratiques Kubernetes/Infisical](security-practices-2026-10-22.md).
- [Kubernetes : modèle NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
- [Kubernetes : admission CEL](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).
- [Flux : isolation multi-tenant](https://fluxcd.io/flux/installation/configuration/multitenancy/).
- [BuildKit : limites rootless](https://github.com/moby/buildkit/blob/master/docs/rootless.md).

Ces documents officiels expliquent les frontières du modèle ; les résultats du
banc d’essai décrivent uniquement les versions et scénarios effectivement testés.

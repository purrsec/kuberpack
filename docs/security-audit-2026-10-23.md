# Audit sécurité Kuberpack — 23 octobre 2026

## Verdict

La passe corrige des chemins d’injection, des fuites de credentials et des
permissions excessives. Elle ne permet pas de qualifier Kuberpack de plateforme
multi-tenant isolant des administrateurs ou des auteurs de manifestes hostiles.

Le risque principal reste **CP compromis → écriture GitOps → Flux privilégié**.
Retirer le shell et limiter le RBAC direct du CP ne supprime pas ce pouvoir
indirect. Le projet/chemin Infisical et la clé API restent également partagés.

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
| Addons | Toutes les apps/addons du projet admis en entrée | Consommateurs déclarés, réplication et dépendance `backup.s3` identifiés ; helpers de dump/bucket correctement étiquetés |
| Image CP | Shell et outils de build présents | Runtime scratch contenant Kuberpack, Git natif/HTTPS et ses bibliothèques ; hooks Git désactivés |
| Dispatcher builder | Création de Jobs dans un namespace Pod Security permissif | Admission API pour Jobs, Pods et `pods/ephemeralcontainers`, images/commandes/arguments et volumes bornés ; privilèges, jetons et accès hôte refusés |
| Credentials | Le PAT GitOps était réellement copié dans le Secret builder | Credential de source privé distinct, registre distinct, séparation bloquante par défaut, absence de repli implicite pour la clé API |
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

2. **Élevé — Secrets et autorisation par projet.** La clé API est administrateur
   global, l’identité/operator Infisical est partagé, et les manifestes utilisent
   le même projet/chemin de secrets. Séparer les identités, permissions et chemins
   avec une migration explicite des clés existantes. Un préfixe de chemin seul
   n’est pas une restriction si l’identité peut encore lire les autres chemins.

3. **Élevé — Ownership des namespaces externes.** Le compilateur empêche le
   partage entre projets Kuberpack et protège les namespaces connus/configurés.
   Il ne prouve pas qu’un namespace arbitraire préexistant est libre ou qu’aucun
   SA externe privilégié n’y réside. Le provisionneur doit refuser l’adoption
   implicite de tels namespaces avant d’y étendre les droits Infisical.

4. **Élevé — Sandbox des builds.** BuildKit garde des exceptions rootless,
   `--oci-worker-no-process-sandbox` et le partage d’un noyau avec le cluster.
   L’admission réduit les possibilités du dispatcher ; elle ne protège pas
   contre une faille du noyau ou de BuildKit. Prévoir des nœuds dédiés ou un
   runtime isolé pour du code hostile. Les tags d’images doivent être épinglés
   en production et les scopes effectifs des credentials vérifiés.

5. **Moyen — Réseau des addons/helpers.** Le refus d’entrée du namespace et les
   policies des services sont actifs ; l’egress des addons/helpers reste ouvert.
   Chaque moteur doit décrire DNS, API/opérateur et sauvegardes avant de fermer
   l’egress du namespace. L’option private-build-egress désactive également une
   protection : préférer ultérieurement une liste privée explicite.

6. **Moyen — Moteur réseau et limites des policies natives.** La présence d’un
   objet NetworkPolicy ne prouve pas son application. Valider EKS/GKE avec leurs
   modes CNI et leur comportement initial. Les policies sont additives ; un autre
   workflow peut rouvrir le réseau. Internet autorisé permet d’atteindre une
   application publique d’un autre projet ; nœud et localhost gardent des
   exceptions au modèle natif.

7. **Moyen — Rotation des secrets runtime.** Le replay sur l’application de test
   déjà lancée a montré qu’une nouvelle valeur synchronisée ne redémarre pas le
   pod recevant sa valeur par variable d’environnement. Le scénario d’installation
   final utilise donc un projet applicatif neuf. Il ne démontre pas une rotation
   automatique des secrets d’un processus existant.

   **Corrigé côté compilation, à revalider sur le banc.** Le compilateur pose
   désormais `secrets.infisical.com/auto-reload` sur le Deployment d’un service
   de projet qui déclare des clés, et le chart `stateless` 0.1.8 reporte
   l’annotation. L’operator Infisical épinglé (0.11.13) pose l’etag du Secret
   synchronisé sur le Deployment et son template de pod, ce qui déclenche le
   rollout ; il ne le fait que si le Secret a réellement changé et si le pod
   consomme le Secret. Aucun composant, ServiceAccount ou droit supplémentaire
   n’est ajouté, et le control plane ne lit toujours aucune valeur. Une
   application sans secret, une preview et une application autonome ne portent
   pas l’annotation. Restent à vérifier sur cluster : le rollout effectif après
   une rotation Infisical, le comportement d’une application à volume
   persistant (stratégie `Recreate`), et la conservation de l’etag de l’operator
   lors des upgrades Helm. La rotation d’un mot de passe côté serveur de bases
   de données reste une opération manuelle des deux côtés.

8. **Moyen — Résilience API/SSRF.** Les limites ajoutées ne constituent pas une
   politique complète de quotas par utilisateur, d’anti-rejeu ou d’egress du CP.
   Une référence de registre arbitraire mérite une revue SSRF et une politique
   d’origines/réseau. Aucun buffer overflow n’a été démontré ; les bibliothèques
   natives et images tierces n’ont pas fait l’objet d’un audit mémoire exhaustif.

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

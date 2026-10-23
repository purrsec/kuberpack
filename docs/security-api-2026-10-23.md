# Audit de la surface API — 23 octobre 2026

L’API possède une authentification globale d’administrateur. Les corrections de cette passe réduisent les entrées dangereuses, les lectures de fichiers hors checkout et la circulation des credentials. Elles ne transforment pas l’API en service multi-utilisateur avec des droits par projet.

Périmètre : handlers HTTP, compilation de `kuberpack.yaml`, clones Git, transmission des credentials aux builds et au registre, lecture des logs et observation Kubernetes. Les preuves ajoutées utilisent des répertoires temporaires, des dépôts Git locaux et des serveurs HTTP de test sur la machine. Le VPS a été arrêté par l’utilisateur et n’a pas été contacté pour cette validation. Ce document décrit le code de travail, pas un déploiement de ces corrections en production.

## Cartographie des entrées

| Entrée | Contrôle présent | Pouvoir obtenu |
| --- | --- | --- |
| `GET /healthz` | Public, réponse fixe | Vérifier que le processus répond |
| `/api/v1/apps`, détails, status, logs et builds | `withAPI`, comparaison du token en temps constant | Observer tous les projets connus |
| Création/compilation, suppression de projet, redeploy | Même token administrateur | Modifier les déclarations et déclencher les pipelines |
| `/hooks/git`, alias `/hooks/forgejo` | Signature HMAC du webhook, secret lié au dépôt | Mettre en file un build ou recompiler le GitOps |
| `/hooks/flux` | HMAC ou bearer dédié | Marquer le dernier build en rollback |
| Contenu GitOps | Accès en écriture au dépôt configuré | Autorité sur les ressources que Flux réconcilie |

Références : [routes et authentification](../internal/control/server.go), [webhook Flux](../internal/control/apps.go), [webhook GitOps](../internal/control/gitops.go).

## Défauts corrigés

| Risque initial | Correction dans le code | Preuve locale |
| --- | --- | --- |
| **Élevé : injection de structure YAML.** Des scalaires du manifeste étaient concaténés dans les fichiers générés. Un `healthcheck` commençant par `/` pouvait aussi contenir un retour à la ligne et introduire `envFrom`. | Quoting de `healthcheck`, chemins/noms de volume, claims, capabilities et `secret_env`. Validation des slugs, chemins, noms PVC et scalaires d’addons ; quantités Kubernetes et nombres d’instances vérifiés. | `TestManifestRejectsGeneratedYAMLInjection`, `TestRuntimeValuesKeepScalarsOutOfGeneratedStructure`, `TestInfisicalEnvironmentRemainsOneScalar`. |
| **Élevé : confusion entre dossiers de projets.** Le compilateur lisait `projects/A/kuberpack.yaml` puis utilisait son champ `project` sans exiger `A`. | Refus quand le champ `project` diffère du dossier lu. Les contrôles de propriété de namespace et de collisions d’identité sont également présents dans `project_security.go`. | `TestReloadRejectsManifestForAnotherDirectory` et tests de propriété de projet. |
| **Élevé : lecture/écriture hors checkout via symlink GitOps.** Un nom de projet nettoyé ne protège pas contre un `kuberpack.yaml` ou un fichier généré qui est un lien versionné. | `rejectRepoSymlinks` vérifie les chemins et leurs ancêtres avant lecture/compilation. Le sous-dossier du build est également résolu et doit rester dans le checkout. | `TestGeneratedTreesRejectSymlinks`, `TestBuildDirectoryCannotEscapeCheckout`. |
| **Élevé : credential GitOps envoyé à un clone public tiers.** `release.Run` réutilisait le token de la plateforme même avec un provider tiers anonyme. | Clone public anonyme, également dans le Job builder. Un clone privé utilise les credentials de son provider et doit appartenir à la même origine HTTP. En-tête Git limité à cette origine, redirections et helper externe `ext` désactivés pour les commandes de checkout. | Tests de `applicationCloneAuth`, clone redirigeant entre deux serveurs HTTP locaux, SHA non valide et helper externe refusés. |
| **Élevé : credential GitOps réutilisé pour inspecter le registre.** Le lookup du digest et le pin d’une version gelée utilisaient `Request.Token`. | `Request.RegistryToken` est transmis par production, preview et alignement de version ; `Run` et `PinExisting` l’utilisent exclusivement. `trigger` lit `KUBERPACK_REGISTRY_TOKEN`. Un build sans credential registre échoue avant son lancement, sans repli vers le token GitOps. | `TestReleaseUsesOnlyTheDedicatedRegistryCredential`, `TestReleaseDoesNotFallBackToTheGitOpsTokenForTheRegistry`, `TestPinExistingUsesOnlyTheDedicatedRegistryCredential` vérifient les headers reçus par un serveur HTTP local. |
| **Élevé : séparation des credentials du builder désactivée par défaut.** | La comparaison avec l’empreinte du credential du CP est bloquante par défaut dans le CP et le builder. L’installation utilise maintenant un credential source builder distinct et refuse les réutilisations de credentials. | `TestCredentialSeparationIsEnforcedByDefault` dans `release` et `buildexec`, tests de séparation de l’installateur. |
| **Moyen : corps de requêtes lents ou trop volumineux.** Le serveur n’avait qu’un délai de lecture des headers. | `ReadHeaderTimeout=5s`, `ReadTimeout=15s`, `IdleTimeout=60s`, headers configurés à 32 Kio. Corps JSON/webhooks lus jusqu’à 1 Mio + 1 octet ; le hook Flux refuse explicitement un dépassement. | `TestAPIBodyReadsAreBoundedBeforeParsingOrCompiling`, `TestAPIRejectsInvalidCredentialBeforeReadingTheBody`. La configuration des délais est vérifiée dans le serveur ; aucun stress test réseau public n’a été effectué. |
| **Moyen : accumulation de connexions de logs.** La limite de cinq réplicas était seulement par requête. | Huit requêtes `follow` simultanées par instance de CP, puis HTTP 429 et `Retry-After`. Chaque écriture de logs a un délai de 15 secondes pour un consommateur bloqué ; ce délai est retiré à la fin de la requête. | `TestRuntimeLogFollowHasAGlobalConnectionLimit`, tests de streaming, déconnexion et réplicas silencieux existants. |
| **Moyen : logs de build bornés après une lecture entière.** Une très grosse ligne pouvait être allouée dans le CP avant le clipping. | Lecture en flux, demande `LimitBytes=200 Kio` au kubelet, borne effective pendant la lecture et délai de 10 secondes. Une réponse qui dépasse la borne est explicitement tronquée. | `TestBuildLogReadIsBoundedEvenForAnOversizedLine`, avec un faux kubelet ignorant la limite demandée. |

Fichiers principaux : [validation](../internal/promote/manifest_validation.go), [rendu des valeurs](../internal/promote/app.go), [rendu Infisical](../internal/promote/infisical.go), [rechargement](../internal/promote/reload.go), [garde des chemins](../internal/promote/repo_security.go), [auth du clone](../internal/release/clone_auth.go), [checkout Git](../internal/fetch/checkout.go), [release](../internal/release/run.go), [pin](../internal/release/pin.go), [serveur HTTP](../cmd/kuberpack/serve.go), [logs runtime](../internal/control/applogs.go), [logs de build](../internal/control/buildlogs.go).

## Contrôles conservés et vérifiés

Les routes métier passent par `withAPI` et refusent un token absent ou incorrect avant lecture du corps. Un token d’API non configuré ferme l’accès. Les routes de logs ne reçoivent pas de namespace ou de sélecteur comme périmètre d’autorisation : le serveur part de l’inventaire, retrouve le Deployment et suit les UIDs Deployment → ReplicaSet → Pod. Les identifiants de build sont relus avec l’identifiant de l’application. Les requêtes SQL correspondantes utilisent des paramètres.

Les compteurs de bridage proviennent d’un endpoint de nœud dérivé des objets Kubernetes, avec vérification du certificat du kubelet, redirections refusées, timeout et réponse bornée à 8 Mio. Aucune route API n’ouvre `exec` dans un pod. Les messages d’échec de conteneur exposés par le status n’incluent pas le contenu arbitraire du fichier de terminaison.

Aucun buffer overflow n’a été démontré dans cette lecture. Cette conclusion ne constitue pas un audit mémoire des binaires Git, du runtime Go, des dépendances ou des images tierces. Les commandes système observées dans le CP utilisent des arguments séparés avec `exec.CommandContext`, sans interpolation d’un manifeste dans une commande shell du CP.

## Résidus et frontières de confiance

1. **Le token API reste administrateur global.** Une personne qui le possède peut lire les logs et les manifestes, compiler, redéployer ou supprimer tous les projets. Il n’existe pas de principal utilisateur, scope par projet, token de lecture seule ou journal d’audit des actions associé à une identité. Les dossiers et NetworkPolicies isolent des workloads ; ils n’autorisent pas à distribuer cette clé à des équipes mutuellement non fiables. Le repli historique vers le token Git a été supprimé de `serve` : sans clé API explicite, les routes administratives restent fermées. Une clé API identique au token Git est refusée ; l’installateur refuse également sa réutilisation avec les credentials du builder ou du registre.

2. **Le chemin de secrets Infisical reste partagé.** Tous les services utilisent `Platform.InfisicalProjectID` et `Platform.InfisicalPath`, et un manifeste choisit ses noms de clés. Un auteur autorisé à soumettre un manifeste peut demander une clé d’un autre projet si elle existe dans ce chemin. La réduction du RBAC de l’opérateur aux namespaces déclarés ne constitue pas une séparation des secrets par projet. Aucun déplacement automatique n’est fait ici, pour préserver les secrets de Stirling et les projets existants. La suite nécessite des chemins ou projets Infisical distincts, des identités aux permissions correspondantes et une migration explicite des clés, par exemple vers `/projects/<project>` ; ajouter seulement un préfixe au YAML ne suffirait pas si l’identité peut toujours lire le reste.

3. **L’écriture GitOps reste un pouvoir de déploiement.** Le CP écrit dans un dépôt réconcilié par Flux et y synchronise les charts de la plateforme. Les protections de chemins empêchent un manifeste de sortir du checkout ; elles ne rendent pas inoffensive une compromission du credential qui écrit le dépôt. Un attaquant qui modifie directement les objets ou les charts dans une partie réconciliée peut obtenir les pouvoirs du contrôleur qui les applique. La séparation des credentials GitOps, les protections des branches, la validation des changements et les comptes de réconciliation Flux limités constituent une autre frontière à traiter.

4. **Une clé distincte ne prouve pas ses permissions effectives.** Les vérifications détectent des credentials identiques, pas leurs scopes réels chez la forge ou le registre. Un second PAT administrateur reste trop puissant. La séparation CP/builder/source/registre doit être assurée par les permissions des credentials fournis. Le mode historique `KUBERPACK_STRICT_CREDENTIALS=false` reste un opt-out explicite à éviter dans une installation durcie.

5. **Les protections DoS restent partielles.** La limite de followers est par processus, sans quota utilisateur. La limite ajoutée par la passe d’egress est par IP, pas par identité : l’API n’a qu’un token administrateur. Il n’y a toujours pas de politique anti-rejeu avec horodatage. La sortie HTTP déclenchée par un registre de manifeste est désormais gardée (adresses privées, boucle locale, métadonnées, noms internes, redirections et rebinding DNS) et une allowlist explicite existe ; voir [Durcissement des accès sortants](security-egress-2026-10-23.md). L’egress réseau du control plane reste optionnel, et les clients des forges ne passent pas par cette garde.

6. **Les logs peuvent contenir les secrets imprimés par l’application.** Les handlers ne lisent pas les Secrets Kubernetes pour produire les logs, mais ne peuvent pas rendre confidentielle une valeur que le processus applicatif écrit lui-même sur stdout. Le droit de lecture des logs doit donc être considéré comme sensible.

## Vérification reproductible

```sh
go test ./internal/control ./internal/release ./internal/fetch ./internal/buildexec ./internal/builder ./internal/promote ./cmd/kuberpack
go test -race ./internal/control ./internal/release ./internal/fetch ./internal/buildexec ./internal/builder ./internal/promote
git diff --check
```

Les tests utilisent uniquement des secrets factices. Aucun envoi de credentials réels vers les serveurs de test et aucun test d’exploitation sur le VPS ou un service externe n’est nécessaire.

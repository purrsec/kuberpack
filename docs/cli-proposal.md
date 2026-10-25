# Kuberpack CLI — proposition de documentation

> Proposition validée du 25 octobre 2026, conservée comme document de conception. L’interface implémentée et ses limites sont décrites dans [le guide CLI](cli.md). Aucune commande décrite ici n'a été implémentée dans le cadre de cet audit. La partie « Guide utilisateur » est volontairement rédigée au présent, comme la documentation du futur produit. Les annexes distinguent le code actuel des changements nécessaires.

## Décisions proposées

1. `kuberpack` devient un client de l'API destiné aux développeurs : connexion, association d'un dossier, déploiement et diagnostic des applications.
2. `kuberpack-admin` porte l'installation, l'administration des projets et des jetons, ainsi que les opérations destructives.
3. Le control plane, le builder et le reloader ont leurs propres exécutables internes, distribués dans leurs images. `serve`, `server`, `builder`, `reloader`, `trigger` et `promote` n'existent pas dans le client développeur, même comme commandes cachées.
4. Le serveur applique les permissions et les protections destructives. Changer le nom ou l'emplacement d'une commande ne constitue pas une protection de l'API.
5. Les sources restent dans Git, les images dans le registre et le déploiement passe par GitOps et Flux. Le CLI ne construit pas localement et ne téléverse pas le dossier de travail.
6. L'utilisation interactive privilégie les commandes courtes : `login`, `init` et `link` demandent uniquement les informations manquantes. L'accueil `kuberpack`, également accessible par `kuberpack wizard`, accompagne les tâches courantes. Les options servent à préremplir les réponses et à automatiser les commandes.

## Guide utilisateur

### À quoi sert le CLI ?

Kuberpack vous permet de déployer et de suivre vos services depuis le terminal. Vous avez besoin de l'URL HTTPS de votre instance et d'un jeton limité à votre projet, fourni par son administrateur.

Un **projet** regroupe des services et leurs dépendances. Un **service** provient d'un dépôt Git ou d'une image publiée. Un **build** transforme un commit Git en image. Un **déploiement** suit une demande jusqu'à l'application de la configuration et à la disponibilité des services concernés.

Le CLI communique avec le control plane distant. Les commandes de développement ne demandent ni kubeconfig, ni accès au registre, ni credential d'écriture GitOps.

### Premier déploiement

L'administrateur a préalablement réservé le projet `demo` et vous a remis un jeton `maintainer` pour ce projet.

```bash
# Ouvrir l'assistant et choisir « Préparer et déployer ce dossier ».
kuberpack

kuberpack status
kuberpack logs --follow
```

L'assistant demande la connexion si elle manque, propose les informations du dépôt, prépare la configuration locale et présente le plan distant. Vous confirmez ce plan pour déployer. Vous n'avez pas à recopier le projet, le service et le dépôt dans une ligne de commande.

Si vous préférez accéder directement à chaque tâche, les commandes restent courtes :

```bash
kuberpack login
kuberpack init
kuberpack deploy
```

`login` demande l'URL de l'instance et le jeton par saisie masquée, puis mémorise la connexion. `init` prépare le manifeste et peut mémoriser le projet réservé ; il n'exige pas qu'un premier service existe déjà. Une fois connecté, les sessions suivantes réutilisent cette connexion.

Le dépôt doit être accessible à la plateforme et les commits doivent être poussés. Les modifications et commits uniquement locaux ne sont pas envoyés. Avant confirmation, le plan affiche les dépôts, branches et SHA distants qui seront utilisés.

Après la première configuration, les pushes sur les branches configurées déclenchent automatiquement les builds. Vous consultez le résultat avec `status`, `builds` et `logs`. `redeploy` permet de reconstruire volontairement un service sans changer son manifeste.

### Catalogue des commandes

```text
kuberpack
  wizard
  login [--url URL] [--context NAME] [--token-stdin]
  logout [--context NAME]
  whoami
  context list | show | use NAME | remove NAME
  init [--project NAME] [--file FILE] [--service NAME] [--repository OWNER/REPO | --image REF]
  link [--project NAME] [--service NAME]
  unlink
  projects list | show NAME
  services list | show NAME
  status [--service NAME] [--watch]
  open [--service NAME]
  validate [--file FILE]
  deploy [--file FILE] [--plan | --plan-id ID] [--yes] [--wait | --detach]
  redeploy [--service NAME] [--commit SHA] [--plan | --plan-id ID] [--yes] [--wait | --detach]
  deployments list | show ID | wait ID
  logs [--service NAME] [--follow] [--tail N] [--since DURATION]
       [--previous] [--timestamps] [--container NAME]
  builds list | show ID | logs [ID | --latest] [--follow]
  completion bash | zsh | fish | powershell
  version
```

Les sous-commandes de lecture `projects`, `services`, `deployments` et `builds` ont une forme canonique unique. En terminal interactif, `kuberpack` sans argument ouvre l'accueil guidé ; hors terminal, il affiche l'aide. Chaque commande accepte `--help`. Les options de `login`, `init` et `link` sont facultatives en interactif ; hors terminal, les informations nécessaires doivent être résolues sans invite.

### Assistant interactif

```bash
kuberpack
# Ou, pour ouvrir explicitement le même accueil :
kuberpack wizard
```

L'accueil présente la connexion et le dossier courants, puis des tâches adaptées : préparer et déployer ce dossier, associer un projet existant, consulter son état ou lire les logs. Un projet déjà associé ne repasse pas par la configuration initiale. L'ouverture du menu seule ne connecte à aucune instance et n'effectue aucune mutation ; les lectures distantes commencent après le choix d'une tâche.

Le menu et les commandes directes utilisent les mêmes formulaires, validations, plans et permissions. Le wizard n'est pas un second CLI et n'a aucun droit supplémentaire. Vous pouvez utiliser `init` la première fois, puis revenir plus tard par le menu ou directement avec `deploy` et `logs`.

L'assistant demande uniquement les informations nécessaires encore inconnues. Le projet imposé par le jeton est affiché, sans demander de retaper son nom. Un dépôt Git `origin` reconnu est proposé comme source ; des remotes ou projets ambigus demandent un choix. La connexion enregistrée et les choix précédents sont réutilisés tant qu'ils correspondent à la cible. Les valeurs détectées ne valent jamais accord pour déployer.

Le récapitulatif regroupe les choix modifiables, avec un accès facultatif aux réglages avancés : port, healthcheck, ressources ou dépendances. Une valeur nécessaire inconnue, notamment une adresse publique autorisée ou un port non détecté, est demandée ; aucun hostname arbitraire n'est inventé pour éviter une question. La détection Git lit les métadonnées sans exécuter le code, les scripts ou les fichiers d'environnement du dépôt.

Exemple de récapitulatif après résolution des informations nécessaires :

```text
Préparer et déployer ce dossier

Connexion  homelab — https://kuberpack.example.com
Projet     demo              (périmètre du jeton)
Source     acme/api          (proposition depuis origin)
Branche    main
Service    web
URL        https://demo.example.com

Continuer vers le plan · Modifier · Réglages avancés · Annuler
```

Choisir « Préparer et déployer » exprime déjà l'intention d'aller jusqu'au plan : l'assistant ne demande pas en plus « voulez-vous lancer deploy ? ». Après enregistrement local, il présente le même plan que `kuberpack deploy`. **La confirmation de ce plan est l'unique autorisation de publication distante.** Un champ prérempli ou le bouton « Continuer vers le plan » ne la remplace pas.

Avant l'enregistrement local, Retour/Escape permet de corriger le brouillon et Ctrl-C l'abandonne sans l'écrire. Si le manifeste ou une association ont déjà été enregistrés, une annulation du plan les conserve et l'annonce explicitement ; elle ne prétend pas annuler une étape terminée. Après acceptation du plan, Ctrl-C arrête uniquement le suivi, comme dans `deploy`.

Le nom canonique de la commande est `wizard`. Elle exige un terminal interactif et refuse `CI=true`, `--non-interactive` et `--output json`, en indiquant les commandes directes utilisables. Elle ne fournit pas de mode `--yes` qui choisirait toutes les réponses à votre place.

### Connexion et contextes

```bash
kuberpack login
kuberpack context list
kuberpack context use homelab
kuberpack whoami
```

`login` demande les informations manquantes et vérifie le jeton auprès de l'instance avant de l'enregistrer. Le nom de connexion est proposé à partir de l'instance et reste modifiable. `--url`, `--context` et `--token-stdin` permettent de préremplir les entrées ou de se connecter sans invite ; aucune de ces options n'est à mémoriser pour l'usage interactif. Le jeton est saisi dans une invite masquée ou lu par `--token-stdin`. Aucun port local n'est ouvert. L'authentification par navigateur n'est pas incluse dans cette version.

`whoami` présente le nom et l'identifiant du jeton, son rôle, son projet, ses éventuelles restrictions de services et son expiration. Il décrit l'identité du credential ; il ne prétend pas identifier un compte utilisateur humain.

Un contexte associe une URL à un credential. Les jetons sont conservés dans le trousseau système. Sans trousseau, l'enregistrement dans un fichier de credentials protégé demande un choix explicite à la connexion ; le répertoire est en `0700` et le fichier en `0600`. Aucune valeur de jeton n'apparaît dans `context show`, `whoami` ou leurs sorties JSON.

Le client développeur demande un jeton de projet. Un credential administrateur se configure dans `kuberpack-admin`, avec un stockage distinct.

`logout` retire le credential local ; il ne révoque pas le jeton côté serveur. `context remove` retire une connexion locale et demande confirmation si des dossiers y sont encore associés. Ces actions ne suppriment aucune ressource distante.

Les connexions utilisent HTTPS avec vérification du certificat. Une autorité privée se configure par `--ca-file`. Il n'existe pas d'option `--insecure` ou de secret passé par `--token`. Le client refuse les redirections de requêtes authentifiées.

### Associer un dossier et choisir la cible

```bash
kuberpack link
kuberpack services list
kuberpack status --service worker
kuberpack unlink
```

`link` propose les projets accessibles puis, s'il y en a, leurs services. Le projet unique autorisé par le jeton est déjà prérempli. La commande vérifie l'existence du projet, les permissions et l'éventuel service sélectionné. Un projet vide peut être associé sans service. Un nom de service préparé localement ne prouve pas son existence distante.

L'association est enregistrée dans la configuration utilisateur, indexée par le chemin du dossier. `link` n'écrit aucun secret ou manifeste dans le dépôt et ne crée aucune ressource distante. `unlink` retire seulement cette association. Les options `--project` et `--service` restent disponibles pour préremplir le choix.

La connexion provient de `--context`, de l'association du dossier ou du contexte actif. Le projet provient des options explicites ou de l'association ; à défaut, `deploy` propose celui du manifeste et les autres commandes celui du périmètre du jeton ou d'un sélecteur. Ces informations sont vérifiées auprès du serveur avant l'action. Le nom du projet dans `kuberpack.yaml` doit correspondre au projet ciblé. Une contradiction provoque une erreur ; aucun projet n'est choisi parce qu'il est le premier d'une liste.

Un changement explicite de contexte ne réutilise pas silencieusement l'association d'un autre contexte : il faut fournir le projet ou refaire `link`. Les noms courts de service, par exemple `web`, sont résolus à l'intérieur du projet. L'identifiant technique actuel `demo-web` reste un détail affichable, sans concaténation approximative côté client.

En interactif, plusieurs choix possibles déclenchent un sélecteur. Hors terminal, une cible ambiguë provoque une erreur. Une commande qui demande un service utilise celui fourni par `--service`, celui du dossier, ou l'unique service du projet ; elle refuse toute autre sélection implicite.

`status` sans service affiche le projet entier. `logs`, `open`, `builds` et `redeploy` demandent un service résolu sans ambiguïté. Avant toute mutation, le CLI affiche l'instance, le projet et les ressources concernées.

### Préparer le manifeste

```bash
kuberpack init
kuberpack validate
kuberpack deploy --plan
```

`init` ouvre le formulaire de préparation du projet. Il propose les informations disponibles et demande les champs manquants, puis présente le manifeste avant son enregistrement local. Les paramètres déjà fournis par des options préremplissent le formulaire ; il n'est pas nécessaire de les donner pour lancer l'assistant.

Pour un projet distant réservé et vide, `init` peut vérifier puis mémoriser l'association **au projet seulement**. Il génère le manifeste du premier service sans exiger un `link` supplémentaire. Le service reste une définition locale tant que l'API ne confirme pas son enregistrement. Il n'est pas nécessaire d'attendre le succès du build pour constater cet enregistrement : une erreur ultérieure peut laisser un service enregistré avec un déploiement échoué.

Pour un projet possédant déjà des services ou des addons, l'assistant propose de l'associer avec `link`. Si vous souhaitez préparer une modification de sa configuration, il propose explicitement de récupérer le **manifeste complet** et de partir de cette base. Il ne génère jamais un fragment à un seul service destiné à remplacer tout un projet. Cette récupération est une lecture distante suivie d'un enregistrement local accepté ; elle ne déploie rien.

La préparation hors connexion reste possible : le CLI crée alors un brouillon local sans prétendre avoir validé ou associé un projet distant. À la connexion suivante, le parcours vérifie l'état réel du projet. Si le brouillon visait un projet vide désormais peuplé, il demande de repartir du manifeste complet ; il ne le traite pas silencieusement comme une mise à jour complète.

`init` refuse d'écraser un fichier existant. `--file` permet de placer le manifeste dans un autre dossier ; il n'est pas obligatoire de l'ajouter au dépôt applicatif. Le control plane ne commite jamais dans ce dépôt. Aucun build, webhook, commit GitOps ou ressource distante n'est créé par `init`.

```yaml
project: demo
services:
  web:
    repository: acme/api
    branch: main
    port: 8080
    hostname: demo.example.com
    healthcheck: /healthz
    replicas: 1
```

`validate` vérifie localement la syntaxe, les champs pris en charge et la cohérence du manifeste, sans requête distante. Cette vérification ne prouve pas les permissions, la capacité disponible ou l'accès aux sources ; le plan serveur effectue ces contrôles.

Les paramètres d'infrastructure du projet sont gérés par l'administrateur : namespace, association Infisical, domaines autorisés, quotas et exceptions de sécurité. Ils ne se modifient pas en soumettant un manifeste développeur.

Les valeurs sensibles restent dans le gestionnaire de secrets. Le manifeste référence leurs clés ; `env` contient uniquement des valeurs non sensibles. Cette version ne fournit pas de commande d'export ou de lecture des valeurs de secrets.

### Déployer une configuration

```bash
kuberpack deploy
kuberpack deploy --file ./deployment/kuberpack.yaml
kuberpack deploy --plan --output json
kuberpack deploy --plan-id plan_123 --yes
```

`deploy` publie **le manifeste complet du projet** et suit le déploiement des services concernés. Il ne s'applique pas à un extrait de manifeste ou à un seul service ; `--service` est refusé pour cette commande. Pour reconstruire un seul service déjà configuré, utilisez `redeploy`.

Il reprend la connexion et le projet préparés par `init` ; aucune étape `link` n'est requise avant le premier déploiement. Si la connexion ou la cible reste inconnue, le mode interactif demande uniquement ce qui manque. Un manifeste absent ne déclenche pas de génération ou de déploiement implicite : la commande propose `init` ou le parcours guidé, sans muter le serveur. Dès que l'API confirme l'enregistrement d'un service, le CLI peut le mémoriser comme service par défaut du dossier s'il est unique ou a été choisi explicitement.

Le CLI valide le fichier puis demande un plan au serveur. Celui-ci présente les ajouts, modifications et retraits de services, les changements de dépendances, les images et digests, ainsi que les commits sources sélectionnés. Aucun commit GitOps, webhook ou build n'est créé pendant cette préparation.

```text
Instance   homelab — https://kuberpack.example.com
Projet     demo
Manifeste  ./kuberpack.yaml

+ web      acme/api@main → 7ac91d2…
  URL      https://demo.example.com

1 service ajouté ; 0 retrait ; aucune suppression de données.
Déployer ce plan ? [y/N]
```

`--plan` affiche ce résultat sans déployer. `--plan-id` exécute exactement un plan déjà préparé ; il est incompatible avec `--file`. L'exécution requiert toujours une confirmation interactive ou `--yes`, obligatoire hors terminal. Le serveur lie le plan au projet, au credential, au contenu du manifeste et aux révisions sélectionnées. Un changement de configuration, de politique ou de permission invalide le plan ; il faut le préparer et le relire à nouveau. Les SHA et digests approuvés ne sont jamais remplacés silencieusement par un nouveau HEAD ou tag.

Une configuration inchangée avec les mêmes révisions ne force pas un nouveau build. Une demande de reconstruction explicite passe par `redeploy`. Les services `image:` utilisent un digest OCI ; ils n'ont pas de build Kuberpack.

Les suppressions de services sans données persistantes sont listées dans le plan et nécessitent leur acceptation explicite. Un retrait ou remplacement d'addon, une suppression ou réduction de volume persistant, un déplacement de namespace et une modification d'identité de secrets sont refusés par cette commande. `--yes` ne permet pas de contourner ce refus. Ces actions passent par une procédure administrateur de migration ou de destruction.

### Reconstruire un service

```bash
kuberpack redeploy --service web
kuberpack redeploy --service web --commit 7ac91d2...
```

`redeploy` reconstruit un service `repository:` à partir de sa configuration distante enregistrée. Sans `--commit`, le serveur résout le HEAD de sa branche configurée au moment de préparer le plan. Avec `--commit`, il vérifie le SHA complet et son appartenance à l'historique autorisé de cette branche. Le plan affiche le commit exact avant confirmation.

Cette commande ne lit pas les changements du manifeste local, ne redémarre pas simplement les pods et ne réutilise pas nécessairement l'image déjà déployée. Une nouvelle demande volontaire crée un nouveau build, même pour le même SHA. Une reprise de la même demande avec le même identifiant d'idempotence ne le duplique pas.

Pour un service `image:`, modifiez sa référence dans le manifeste puis lancez `deploy`. `redeploy` explique cette action et refuse de lancer un build inexistant.

### Savoir quand le déploiement est terminé

`deploy` et `redeploy` attendent le résultat par défaut, en terminal comme en automatisation. `--wait` rend ce choix explicite. `--detach`, incompatible avec `--wait`, rend la main après acceptation et affiche l'identifiant de déploiement.

```bash
kuberpack deploy --detach
kuberpack deployments show dep_123
kuberpack deployments wait dep_123 --timeout 15m
```

L'attente suit la demande identifiée : révisions sources, builds, digests, commit GitOps, réconciliation et disponibilité des services concernés. L'ancienne version encore saine ne valide pas un nouveau déploiement. Pour une image publiée ou un changement de configuration seul, la phase de build est absente.

Le succès signifie que les révisions attendues sont appliquées et que la génération correspondante est prête. Si le manifeste demande zéro réplica, le résultat attendu est `stopped` après convergence à zéro. Un build réussi ou une réponse HTTP `202` ne suffit pas. Une erreur de build, un rollback ou un échec de réconciliation donne un échec de la commande.

Le délai par défaut est de quinze minutes ; `--timeout` le remplace. Un dépassement du délai ou Ctrl-C arrête l'observation locale, sans annuler le travail distant. La sortie conserve l'identifiant permettant de reprendre l'attente. Une demande remplacée par une plus récente apparaît comme `superseded` et ne peut pas devenir un faux succès.

### État et diagnostic

```bash
kuberpack projects list
kuberpack projects show demo
kuberpack services list
kuberpack services show web
kuberpack status --service web
kuberpack status --watch
kuberpack open --service web
```

`status` présente la disponibilité, les réplicas, l'image observée, la réconciliation et les derniers échecs. Le détail d'un service inclut CPU/RAM, demandes et limites, redémarrages et disponibilité des mesures. Une mesure indisponible reste inconnue, sans zéro inventé. Le bridage CPU est présenté comme un cumul sur la vie des conteneurs actuels.

Les états de disponibilité sont `running`, `deploying`, `pending`, `degraded`, `failed`, `stopped` et `unknown`. Le statut d'un build ou d'une demande de déploiement reste distinct.

Consulter un service en échec avec `status` retourne un succès si la lecture a fonctionné. Pour un contrôle bloquant de livraison, utilisez `deployments wait`. `open` ouvre uniquement l'URL HTTPS déclarée du service et ne demande aucune modification distante.

### Logs d'exécution et de build

```bash
kuberpack logs --service web --tail 100
kuberpack logs --service web --follow --since 10m --timestamps
kuberpack logs --service web --previous

kuberpack builds list --service web
kuberpack builds show 42 --service web
kuberpack builds logs 42 --service web
kuberpack builds logs --latest --service web --follow
```

`logs` lit les conteneurs de l'application. `builds logs` lit le builder. Les services issus d'une image publiée disposent de logs d'exécution et d'un historique de déploiement, mais d'aucun build Kuberpack.

Sans `--follow`, les logs d'exécution affichent les cent dernières lignes par réplica. `--tail`, `--since`, `--previous`, `--timestamps` et `--container` conservent leur sens explicite. `--previous` lit l'instance précédente dans un pod encore présent, pas les pods supprimés.

Le suivi runtime porte sur les pods trouvés au démarrage du flux ; un remplacement de pods exige de relancer la commande. Les limites du serveur, dont le maximum actuel de cinq réplicas lisibles simultanément, produisent un message explicite. Les identifiants de pod servent à présenter les lignes, jamais à choisir librement un autre périmètre Kubernetes.

`builds logs --latest` résout une fois le dernier build du service. Le suivi reste attaché à cet identifiant et se termine avec ce build. Il ne bascule pas vers un build arrivé entre-temps. Une coupure ou une erreur de flux donne un code non nul.

Les sorties contrôlées par Kuberpack masquent les credentials connus et neutralisent les séquences de contrôle du terminal. Les logs peuvent cependant contenir des secrets imprimés par votre code ; leur lecture est un droit sensible, et le CLI ne garantit pas une anonymisation de leur contenu.

### Options communes et automatisation

| Option | Contrat |
| --- | --- |
| `--context NAME` | Sélectionner la connexion. |
| `--project NAME` | Sélectionner le projet ; doit correspondre au manifeste lors d'un `deploy`. |
| `--service NAME` | Sélectionner un service pour les commandes qui l'acceptent. |
| `--output text\|json` | Choisir le format ; ne confirme aucune action. |
| `--non-interactive` | Interdire invites, sélecteurs et ouverture implicite de navigateur. |
| `--yes` | Accepter le plan autorisé d'une mutation ; ne change aucun droit. |
| `--timeout DURATION` | Borner l'attente ou une opération finie. |
| `--request-id ID` | Identifier une mutation pour permettre sa reprise sans duplication. |
| `--help`, `--version` | Consulter l'aide ou la version. |

Les options se placent avant ou après les arguments de ressource. Les combinaisons inconnues ou incompatibles échouent sans mutation. `--yes` n'est accepté que par les commandes qui confirment effectivement une action.

Hors TTY, avec `CI=true` ou avec `--non-interactive`, aucune invite n'est autorisée. Les cibles et les paramètres nécessaires doivent être fournis ou résolus sans ambiguïté à partir d'une configuration déjà enregistrée. Une valeur manquante produit une erreur indiquant son option, sans lecture de stdin implicite ; seules les entrées explicites telles que `--token-stdin` lisent stdin. `--yes` accepte une action renseignée mais ne complète jamais un formulaire et ne choisit aucune cible. Une mutation exige `--yes` ; la présence de JSON ou la détection d'un agent ne vaut jamais confirmation. `--output json` impose également l'absence d'invites et exige `--yes` pour une mutation. `wizard` refuse ces modes ; `kuberpack` sans argument y affiche simplement l'aide.

Un résultat fini produit un document JSON sur stdout. Un suivi produit du NDJSON, un objet par ligne, avec un type d'événement et des identifiants. La progression et les diagnostics vont sur stderr. Une erreur finie en mode JSON produit une enveloppe `error` structurée, sans credential ; le code de sortie reste non nul. `--output json` ne révèle jamais un secret de configuration.

```json
{"schema_version":1,"deployment_id":"dep_123","project":"demo","status":"succeeded","services":[{"name":"web","commit_sha":"7ac91d2...","state":"running"}]}
```

Un résultat détaché porte `status: accepted`. Un événement de flux expose au minimum `schema_version`, `type`, `timestamp` et l'identifiant de la ressource concernée. Les évolutions compatibles ajoutent des champs sans changer le sens des champs existants.

| Code de sortie | Sens |
| --- | --- |
| `0` | Commande réussie ; en mode détaché, demande acceptée seulement. |
| `1` | Erreur distante, réseau ou interne non classée. |
| `2` | Syntaxe, cible ambiguë, fichier invalide ou confirmation manquante. |
| `3` | Authentification refusée. |
| `4` | Permission ou politique serveur refusée. |
| `5` | Déploiement échoué, rollback ou demande remplacée. |
| `6` | Délai d'attente dépassé. |
| `130` | Interruption locale par Ctrl-C. |

### Utiliser le client dans un pipeline

Un pipeline externe peut utiliser le même **client HTTP** avec un jeton `deployer` limité à un service. Il n'exécute pas le builder de la plateforme et ne reçoit aucun accès Kubernetes ou GitOps. L'automatisation de la publication du manifeste nécessite séparément le rôle `maintainer` et n'est pas le rôle d'un pipeline qui reconstruit seulement du code.

Les variables `KUBERPACK_API_URL` et `KUBERPACK_API_TOKEN` sont injectées ensemble par le gestionnaire de secrets du runner. Si l'une est présente, l'autre doit l'être ; ce mode éphémère n'emprunte aucun credential au contexte local et ne les persiste pas. Il est incompatible avec `--context`.

```bash
# CI_COMMIT_SHA et CI_PIPELINE_ID viennent du fournisseur CI.
# URL et jeton de projet sont injectés comme variables protégées du job.
kuberpack redeploy \
  --project demo --service web \
  --commit "$CI_COMMIT_SHA" \
  --request-id "pipeline-$CI_PIPELINE_ID-web" \
  --non-interactive --yes --wait --timeout 15m --output json
```

Les lectures transitoires peuvent être retentées avec backoff et respect de `Retry-After`. Une écriture n'est retentée qu'avec le même identifiant d'idempotence et le même contenu. Après une interruption réseau, une absence de réponse n'est jamais présentée comme la preuve d'une absence de mutation.

### Fonctionnalités volontairement absentes

Le client ne propose pas `serve`/`server`, d'option d'écoute réseau, de serveur MCP implicite, de build local, de soumission directe de Job Kubernetes ou de modification directe d'un digest GitOps. Il ne fournit pas de commande générique permettant d'appeler arbitrairement toutes les routes d'administration.

Cette version n'ajoute pas de shell distant, `exec`, connexion SQL, redémarrage direct des pods, rollback manuel, gestion de valeurs de secrets ou environnements `staging`/`production` arbitraires. Ces fonctions demandent un contrat produit et une API propres. Le champ `secret_env` sélectionne une source de secrets ; il ne définit pas un environnement de déploiement. Les previews existantes restent pilotées par les PR, sans nouvelle commande de gestion dans ce périmètre.

## Administration et composants internes

### Outil administrateur

`kuberpack-admin` est distribué séparément. Son aide distingue les opérations de bootstrap utilisant le kubeconfig et celles utilisant l'API d'administration.

```text
kuberpack-admin login --url URL --context NAME [--token-stdin]
kuberpack-admin install --context KUBE_CONTEXT --config FILE [--plan | --yes]
kuberpack-admin doctor network --context KUBE_CONTEXT [--detect-only | --yes]
kuberpack-admin projects create NAME
kuberpack-admin projects configure NAME --file FILE [--plan | --yes]
kuberpack-admin projects destroy NAME --plan
kuberpack-admin projects destroy NAME --plan-id ID --confirm-project NAME [--yes]
kuberpack-admin tokens create --name NAME --role ROLE --project NAME [--service NAME] --expires DURATION --output-file FILE
kuberpack-admin tokens create --name NAME --role admin --expires DURATION --output-file FILE
kuberpack-admin tokens list
kuberpack-admin tokens revoke ID [--yes]
```

`install` exige un contexte Kubernetes explicite et présente l'adresse du cluster avant confirmation. Les secrets d'installation sont lus depuis des entrées protégées, des variables ou des fichiers de credentials ; ils ne sont pas des options contenant leur valeur dans argv. Un plan ne modifie pas le cluster.

`projects create` réserve un projet vide, sans application implicite. `projects configure` associe les politiques opérateur, les quotas et le périmètre de secrets au projet. Les changements d'identité ou de stockage exigent une procédure de migration ; cette commande ne les applique pas comme de simples changements de configuration.

Le nom `--context` désigne ici le contexte Kubernetes uniquement pour `install` et `doctor network`, et une connexion API pour les commandes distantes. Chaque aide et chaque confirmation nomment explicitement ce type ; aucun contexte Kubernetes n'est utilisé par repli pour une opération API.

### Permissions des jetons

| Rôle proposé | Périmètre et droits |
| --- | --- |
| `reader` | Un projet : inventaire, état, builds, déploiements et logs. Le droit de lecture des logs est inclus explicitement. |
| `deployer` | Droits `reader` et reconstruction des services `repository:` déjà enregistrés ; restriction possible à des services précis. Aucune publication de manifeste. |
| `maintainer` | Droits `deployer` et publication du manifeste dans les limites du projet. Aucun pouvoir de destruction de données, de changement de namespace ou d'identité de secrets. |
| `admin` | Administration globale, création/révocation des jetons et procédures destructives. Usage réservé à l'outil administrateur. |

La création exige un rôle et une durée positive explicites. `--project` est obligatoire pour les trois rôles de projet et interdit pour `admin`. `--service`, répétable pour un jeton `deployer`, restreint les services autorisés ; son omission donne accès aux services du projet, jamais aux autres projets. L'omission d'une option ne crée jamais un administrateur. Le serveur impose ces règles et les restrictions de services, y compris pour un client HTTP écrit à la main.

Le secret d'un nouveau jeton est écrit une seule fois vers `--output-file`, créé exclusivement en `0600` ; une destination existante est refusée. L'API n'enregistre que son empreinte et la commande n'affiche que des métadonnées. Un échec d'écriture doit permettre de révoquer le jeton créé et signaler son identifiant sans le secret. La rotation et la révocation sont des actions explicites.

### Destruction d'un projet

La destruction est réservée à `admin`. Le plan recense les services, addons, PVC, volumes attachés, namespace, synchronisations de secrets et ressources externes concernées. Une impossibilité de lire ce périmètre bloque l'opération.

Un projet possédant des données persistantes ne peut pas être détruit par ce CLI dans cette version. Une procédure opérateur distincte doit d'abord sauvegarder, migrer ou décommissionner ces données, puis établir côté serveur qu'il ne reste aucun stockage à détruire. Le plan affiche ce blocage ; aucun `--force` ne le neutralise.

Pour un projet sans données, l'exécution exige un plan valide et le nom exact avec `--confirm-project`. Hors TTY, `--yes` est également requis. Le serveur revérifie les droits et l'inventaire au moment de l'exécution. La suppression du namespace n'est possible que si sa propriété et son absence de ressources persistantes ou étrangères sont vérifiées ; sinon elle reste bloquée.

### Exécution du control plane et des workers

| Composant | Distribution et fonction |
| --- | --- |
| Client développeur `kuberpack` | Binaire poste de travail, client API uniquement. |
| Outil `kuberpack-admin` | Distribution opérateur, API admin et bootstrap Kubernetes explicites. |
| `kuberpack-control-plane` | Entrypoint de l'image serveur, API/webhooks/file/réconciliation. |
| `kuberpack-builder` | Entrypoint des Jobs de build ; reçoit le contrat de build et ses credentials minimaux. |
| `kuberpack-reloader` | Agent interne avec son compte de service, uniquement si ce composant est utilisé. |

Le control plane conserve un listener HTTP interne au Pod pour permettre les Services, sondes et Ingress. Une écoute sur toutes les interfaces du **Pod** peut être nécessaire ; elle ne justifie aucune commande serveur dans le client développeur. Le packaging utilise un Service `ClusterIP`, sans `hostNetwork`, `hostPort` ou NodePort, et publie l'API uniquement derrière l'Ingress HTTPS prévu.

La surface réseau et les réglages de débit, proxies de confiance, egress et stockage relèvent de la configuration opérateur. La distribution cliente ne contient ni ce listener ni les fonctions des workers. Les images et leurs points d'entrée doivent donc évoluer ensemble ; retirer seulement une ligne de l'aide ne répond pas à cette séparation.

## Annexe A — audit du control plane actuel

### Référence et méthode

Lecture statique du dépôt Kuberpack à la révision `aebd072d8b5dfb8e8fb9daef0e5bcfaeabe1359c`, avec un arbre de travail initialement propre. Aucun appel à l'instance réelle, au cluster, à une forge authentifiée ou à un registre. Les tests existants ont été lus comme éléments du contrat ; ils n'ont pas été exécutés pour cette modification documentaire.

Le routeur de référence est [internal/control/server.go](../internal/control/server.go#L195). Il enregistre **19 couples méthode/chemin** : 15 routes métier sous `/api/v1`, trois webhooks et une sonde. Les requêtes `HEAD` peuvent également utiliser les patterns `GET` selon le routeur Go. Aucun endpoint d'ancienne documentation n'est présumé disponible sans enregistrement dans ce routeur.

### Inventaire exhaustif des routes enregistrées

Dans ce tableau, **projet** désigne le rôle actuel `project`, qui cumule lecture et pilotage. L'administrateur global a accès à toutes les routes métier.

| Méthode et chemin actuel | Accès réel et comportement | Place dans le futur CLI |
| --- | --- | --- |
| `GET /healthz` | Anonyme, réponse `ok`, hors rate limit ; ce n'est pas une vérification complète des dépendances. | Sonde de plateforme, pas preuve de disponibilité applicative. |
| `GET /api/v1/apps` | Jeton requis ; apps Git et images publiées filtrées par projet, avec observations légères. | `services list`, `status`. |
| `GET /api/v1/apps/{name}` | Jeton autorisé sur l'app ; `404` pour une app hors périmètre. Métadonnées et déploiement observé. | `services show`, `open`. |
| `GET /api/v1/apps/{name}/status` | Même périmètre ; disponibilité et ressources observées. | `status --service`. |
| `GET /api/v1/apps/{name}/logs` | Même périmètre, y compris `image:` ; flux runtime, options bornées, résolution des pods côté serveur. | `logs`. |
| `GET /api/v1/apps/{name}/builds` | Même périmètre ; liste SQLite. Tableau vide pour une image publiée. | `builds list`. |
| `GET /api/v1/apps/{name}/builds/{id}` | App autorisée puis build relu sous l'identifiant de cette app. | `builds show`. |
| `GET /api/v1/apps/{name}/builds/{id}/logs` | Même contrôle ; texte conservé ou instantané lu dans le pod du builder. | `builds logs`, avec adaptation pour le suivi. |
| `POST /api/v1/apps/{name}/redeploy` | Projet autorisé ; résout le HEAD de la branche configurée et enfile un nouveau build à chaque appel. Réponse `202` avec statut et SHA, sans identifiant de déploiement. Refus pour `image:`. | `redeploy`, après ajout des permissions, plan et suivi de demande. |
| `POST /api/v1/tokens` | Admin uniquement. Projet vide ⇒ rôle admin ; expiration absente ⇒ jeton sans expiration. Secret retourné une fois. | `kuberpack-admin tokens create`, avec contrat plus strict. |
| `GET /api/v1/tokens` | Admin uniquement ; métadonnées, sans secret ou empreinte. | `kuberpack-admin tokens list`. |
| `DELETE /api/v1/tokens/{id}` | Admin uniquement ; révocation idempotente d'un identifiant positif. | `kuberpack-admin tokens revoke`. |
| `POST /api/v1/projects` | Admin ou jeton du projet nommé dans le manifeste. Création **et mise à jour**, compilation GitOps, provisioning tenant et réconciliation des apps. Réponse `201` après ces étapes. | Futur `deploy` et création admin, à séparer dans le contrat serveur. |
| `GET /api/v1/projects` | Projets lus dans GitOps puis filtrés par périmètre. | `projects list`. |
| `GET /api/v1/projects/{name}` | Admin ou jeton du projet ; renvoie le manifeste **YAML brut**, pas un objet JSON projet/services. | `projects show`, après adaptation/normalisation. |
| `DELETE /api/v1/projects/{name}` | Admin **ou jeton du projet** ; `force=true`/`1` contourne le contrôle des addons. Retrait GitOps, inventaire, webhooks et nettoyage tenant. | Retirée du contrat développeur ; administration seulement, sans bypass générique. |
| `POST /hooks/forgejo` | Pas de Bearer API ; signature HMAC pour les dépôts enregistrés. Push et PR. | Intégration forge interne. |
| `POST /hooks/git` | Alias du même handler que `/hooks/forgejo`. | Endpoint canonique de l'intégration forge, aucun verbe CLI public. |
| `POST /hooks/flux` | Secret Flux distinct, HMAC ou Bearer ; traduit certaines notifications d'erreur/rollback en état de build `rolled_back`. | Intégration Flux interne ; ne commande pas un rollback. |

Sources des handlers : [authentification et inventaire](../internal/control/server.go#L295), [autorisation](../internal/control/authorization.go), [projets](../internal/control/projects.go), [builds/redeploy/Flux](../internal/control/apps.go), [jetons](../internal/control/apitokens.go), [logs runtime](../internal/control/applogs.go), [status](../internal/control/appstatus.go).

### Constats prioritaires

| Priorité | Constat vérifié | Conséquence pour la proposition |
| --- | --- | --- |
| Haute | Le jeton `project` peut publier tout le manifeste et supprimer son projet ; il n'existe que les rôles `admin`/`project`. [Contrôle de périmètre](../internal/control/authorization.go#L35), [suppression](../internal/control/projects.go#L108), [test du contrat](../internal/control/apitokens_test.go#L302). | Séparer lecture, reconstruction, publication de manifeste et destruction côté serveur. Un jeton CI ne doit pas pouvoir détruire un projet. |
| Haute | `DeleteProject` bloque les addons seulement si leur lecture réussit ; une erreur est ignorée par la condition. La vérification ne recense pas les volumes persistants des services. [Garde de suppression](../internal/promote/project.go#L1305), [volumes du manifeste](../internal/promote/project.go#L107). | Inventorier tous les stockages et bloquer sur erreur ; supprimer le bypass `force`. C'est un constat de contrôle de flux, sans exploitation réalisée. |
| Haute | En mode `TenantIsolation`, la suppression peut détruire le namespace portant le label du projet. [Nettoyage tenant](../internal/control/provision.go#L210). | Ne jamais présenter cette opération comme un simple désenregistrement ; réserver la destruction à l'administrateur et vérifier l'inventaire réel. |
| Haute | Omettre `project` crée un jeton administrateur ; omettre `expires_in` le rend non expirant. [Création API](../internal/control/apitokens.go#L51), CLI actuel (`cmd/kuberpack/token.go:45`, révision `aebd072`). | Rôle et expiration obligatoires, destination du secret explicite, aucune élévation par omission. |
| Haute | Un auteur de manifeste choisit les sources, images et références de secrets. Le mode strict existant contraint `auth_ref` et `path`, mais le `project_id` reste fourni par le manifeste ; l'activation et les permissions Infisical effectives comptent. [Validation](../internal/promote/infisical.go#L239), [repli de scope](../internal/promote/infisical.go#L217). | Lier l'identité de secrets au projet dans une politique administrateur côté serveur. Une séparation de binaires ne résout pas cette frontière. |
| Haute | `serve` écoute par défaut `:8080` et utilise `ListenAndServe`, sans TLS natif. Options (`cmd/kuberpack/serve.go:34`, révision `aebd072`), listener (`cmd/kuberpack/serve.go:204`, révision `aebd072`). | Sortir entièrement le serveur du client. Distinguer une écoute du Pod derrière Ingress d'une exposition HTTP sur le poste de travail. |
| Moyenne | Le manifeste accepte `run_as_root`, `writable_root` et des capabilities dont la validation contrôle la forme. Le chart les transmet au conteneur ; l'admission Kubernetes reste une barrière indépendante. [Validation](../internal/promote/manifest_validation.go#L141), [chart](../provisioning/charts/stateless/templates/deployment.yaml#L97). | Toute exception relève d'une autorisation opérateur par projet/service, sans nouveau flag permettant de désactiver la sécurité depuis le CLI. Ce constat ne démontre pas de contournement de l'admission. |
| Moyenne | `project compile --dry-run` valide et réimprime seulement le YAML local ; il n'obtient pas un diff serveur. Le POST réel écrit GitOps et peut ensuite échouer au provisioning. CLI (`cmd/kuberpack/project.go:304`, révision `aebd072`), [étapes API](../internal/control/projects.go#L40). | Vrai plan distant, suivi des étapes et résultat partiel explicite ; ne jamais annoncer une transaction atomique inexistante. |
| Moyenne | Chaque redeploy génère un nonce ; la réponse ne fournit ni ID de livraison ni ID de build. [Redeploy](../internal/control/apps.go#L83). | Identifiant stable, idempotence et corrélation build/déploiement avant d'offrir un `--wait` fiable. |
| Moyenne | Les erreurs HTTP utilisent principalement du texte et des codes génériques ; les listes de projets lisent GitOps, les états de workloads ne sont pas liés à une demande. | Erreurs structurées, contrat versionné et attentes corrélées. Ne pas interpréter le dernier build ou l'ancienne application saine comme résultat de sa propre demande. |
| Moyenne | Le chart peut exposer un NodePort en option et l'Ingress actuel route `/` vers le même serveur. [Service/Ingress](../provisioning/charts/kuberpack/templates/service.yaml). | API métier, webhooks et administration doivent avoir une politique d'exposition explicite ; une URL commune ne remplace pas leurs contrôles propres. |

### Protections existantes à conserver

L'authentification des routes métier refuse un credential absent, inconnu, expiré ou révoqué avant le handler. Le jeton Git n'est plus un repli de jeton API ; `serve` refuse les deux valeurs identiques. Les secrets des jetons créés sont aléatoires et seule leur empreinte SHA-256 est stockée. [Middleware](../internal/control/server.go#L302), configuration (`cmd/kuberpack/serve.go:67`, révision `aebd072`), [stockage](../internal/store/apitokens.go).

Les applications sont filtrées par projet et les apps d'un autre projet sont masquées par `404`. L'administration des jetons est déjà réservée à admin. Un build est recherché sous son application ; les pods de logs sont retrouvés par leur chaîne de propriétaires et non par un namespace ou sélecteur fourni par le client. [Autorisation](../internal/control/authorization.go), [builds](../internal/control/apps.go#L182), [pods](../internal/control/apppods.go).

Les corps sont bornés à 1 Mio, les lectures et flux de logs ont des bornes, et un limiteur intervient avant le routeur, hors sonde. Les proxies de confiance sont déclarés explicitement. La limitation générale est par IP, pas par identité ; elle ne remplace pas un quota de builds par projet. [Corps JSON](../internal/control/server.go#L753), [limiteur](../internal/control/ratelimit.go), [logs](../internal/control/applogs.go).

La validation des chemins et namespaces, le refus de `defaultDeny: false` et les contraintes Infisical strictes existantes restent utiles. Leur application dépend aussi de la configuration déployée ; cet audit du dépôt ne démontre pas les valeurs actives sur l'instance réelle.

### Écarts avec la documentation actuelle

Le fichier [vision.md](vision.md) évoque encore `POST /api/v1/apps`, absent du routeur. [api.md](api.md) décrit le GET d'un projet comme « un projet et ses services », alors que le handler retourne le manifeste YAML brut. [architecture.md](architecture.md) contient encore des interdictions d'accès Kubernetes du control plane incompatibles avec ses fonctions actuelles d'observation et de provisioning.

L'exemple de [README.md](../README.md) réutilisant `FORGEJO_TOKEN` comme `KUBERPACK_API_TOKEN` est désormais rejeté par `serve`. Ces documents devront être alignés lors de l'implémentation approuvée ; ils ne sont pas modifiés par cette proposition.

## Annexe B — audit du CLI actuel et migration

Le CLI distribue aujourd'hui onze entrées au même niveau : `promote`, `trigger`, `install`, `serve`, `project`, `apps`, `app`, `token`, `reloader`, `doctor`, `builder`. Dispatcher et aide (`cmd/kuberpack/main.go`, révision `aebd072`).

| Interface actuelle | Destination proposée | Traitement |
| --- | --- | --- |
| `project init` | `kuberpack init` | Formulaire sans flags obligatoires en TTY ; manifeste local et association possible au projet vérifié, sans exiger de service distant. |
| `project create` | `init`, puis `deploy` | Retrait du mélange génération/publication ; réservation initiale du projet par admin. |
| `project compile` / `project apply` | `kuberpack deploy` | Nommer l'effet réel ; ajouter plan, permissions et suivi. |
| `project compile --dry-run` | `kuberpack validate` | Équivalent de la seule validation locale ; `deploy --plan` est une nouvelle capacité serveur. |
| `apps` | `kuberpack services list` | Vocabulaire cohérent avec le manifeste. |
| `app status NAME` | `kuberpack status --service NAME` | Cible liée au dossier ou explicite. |
| `app logs NAME` | `kuberpack logs --service NAME` | Logs d'exécution uniquement. |
| `app builds` / `app build-logs` | `kuberpack builds list` / `builds logs` | Regroupement de l'historique et des logs de build. |
| `app logs --build` | `kuberpack builds logs` | Retrait de la surcharge runtime/build. |
| `app redeploy` | `kuberpack redeploy` | Reconstruction distante, jamais présentée comme restart. |
| `project delete` | `kuberpack-admin projects destroy` | Retrait du client développeur et restriction côté API. |
| `token create/list/revoke` | `kuberpack-admin tokens …` | Rôle/expiration explicites, secret vers destination protégée. |
| `install` / `doctor network` | `kuberpack-admin install` / `doctor network` | Contexte cluster obligatoire ; distinguer lecture et probes qui écrivent. |
| `serve` | Image `kuberpack-control-plane` | Aucune commande ou alias dans le client. |
| `builder` / `reloader` | Exécutables internes dédiés | Mettre à jour points d'entrée, charts et Jobs ensemble. |
| `trigger` / `promote` | Retrait du produit développeur | Le control plane pilote les builds et la promotion. Toute maintenance interne restante demande un outil opérateur séparé, sans alias public. |

Les aliases transitoires ne concernent que les commandes développeur sans changement de pouvoir. Aucun alias de `serve`, `trigger`, `promote` ou de suppression ne reste dans le nouveau binaire. Les anciens clients ne doivent pas conserver l'ancien pouvoir destructif via `/api/v1` : le serveur doit refuser cette opération pour un jeton de projet, quelle que soit la version du client.

Les profils actuels `clusters/*.env` ne sont ni sourcés automatiquement ni copiés comme credentials développeur : ils peuvent contenir un jeton administrateur. L'opérateur émet de nouveaux jetons de projet, puis chaque utilisateur se connecte explicitement. Les jetons existants `project` ne sont pas implicitement assimilés aux futurs `maintainer` ; leur remplacement et leur révocation font partie de la migration.

Le CLI actuel comporte déjà un questionnaire partiel dans runProjectWizard (`cmd/kuberpack/project.go:58`, révision `aebd072`). La correction proposée améliore donc aussi la présentation et le contrat de l'expérience : commandes courtes en premier, questions nécessaires seulement et assistant de tâches partageant les mêmes opérations.

## Annexe C — capacités serveur nécessaires avant livraison

Cette proposition n'est pas un simple renommage de commandes. Les routes actuelles suffisent pour une bonne partie de la lecture, mais pas pour les garanties de publication, de permissions et d'attente décrites dans le guide.

| Capacité | État actuel | Contrat à fournir |
| --- | --- | --- |
| Session et capacités | Aucun endpoint d'identité/version métier. | `GET /api/v2/session` : identité du credential, scopes, expiration, version et capacités ; aucune valeur de secret. |
| Inventaire projet/service | Apps globales et manifeste YAML. | DTO projet/service avec identité stable, nom court, type et cible réelle. Ne pas reconstruire les identités en découpant les noms. |
| Permissions | `admin` ou `project`. | `reader`/`deployer`/`maintainer`/`admin`, restriction optionnelle de services, politiques administrateur du projet, contrôles également sur l'ancienne API. |
| Réservation et configuration opérateur | POST unique de compilation. | Contrat admin distinct pour réserver le projet, associer namespace/secrets/domaines/quotas et encadrer les migrations. |
| Plan de manifeste | Aucun diff distant sans mutation. | `POST /api/v2/projects/{project}/plans` : action typée, validation autoritative, révision de base, inventaire d'impact, SHA/digests résolus et plan expirant. |
| Exécution d'un plan | POST synchrone multi-étapes sans transaction globale. | `POST /api/v2/projects/{project}/deployments` avec `plan_id` et clé d'idempotence : vérification des droits, verrou/concurrence et retour rapide avec ID stable. Aucun nouveau HEAD implicite. |
| État d'une demande | Builds app-scopés ; pas d'opération globale. | Liste/GET de `/api/v2/projects/{project}/deployments[/{id}]` : liens builds, digests, commit GitOps, réconciliation et génération observée ; étapes, erreurs partielles et état terminal. |
| Reprise | Nonce à chaque redeploy ; aucun ID retourné. | Même clé + même contenu ⇒ même demande ; même clé + contenu différent ⇒ `409`. Persister l'opération avant effets et permettre de la retrouver après une coupure. |
| Suivi de build | Logs instantanés sur une route GET. | Suivi cadré avec curseur/offset ou polling sans doublons et fin explicite ; rattachement immuable au build sélectionné. |
| Destruction | Jeton projet autorisé ; `force` disponible. | Admin seulement, inventaire complet et échec sur lecture impossible, plan lié à la révision et blocage des données persistantes ; anciens chemins sans bypass. |
| Erreurs et quotas | Texte libre, rate limit par IP. | Erreurs structurées, request ID, conflits `409`, expiration de plan et limites de builds/concurrence par projet/identité, en conservant les bornes globales. |
| Journal d'actions | Dates d'utilisation des tokens, pas de journal métier complet. | Credential, action, projet, ressources, révisions, résultat et identifiant de demande ; aucune valeur de secret. |

Les noms `/api/v2` ci-dessus constituent une proposition de contrat, pas des routes existantes. Les anciennes routes de lecture peuvent être adaptées ; le nouveau client doit refuser une mutation si le serveur n'annonce pas les garanties nécessaires. Aucun repli silencieux vers une ancienne route permissive.

Le plan est temporaire, lié à son identité et à une empreinte de politique ; il est invalidé si la base ou les droits changent. L'exécution compare la révision au moment de réserver le travail. Le serveur sérialise les modifications concurrentes par projet et corrèle les pushes automatiques aux demandes existantes ; une nouvelle révision externe peut rendre une demande `superseded`.

Le suivi ne promet pas une transaction entre Git, Kubernetes, forge et SQLite. Il expose les étapes réellement effectuées, permet une reprise idempotente et distingue échec avant écriture et échec partiel après commit GitOps. Il ne valide jamais une opération par le seul état `running` d'une ancienne version.

Les webhooks restent une surface machine distincte : secrets propres, taille bornée, validation de signature, déduplication et politique d'exposition. Le retrait des verbes internes du CLI ne les transforme pas en actions de développeur et ne les protège pas à lui seul.

L'assistant n'exige pas de route `wizard`. Il compose les lectures de session/projet/manifeste, les écritures locales et les plans déjà prévus. L'état « projet vide » et la révision du manifeste récupéré doivent être revérifiés avant publication pour éviter qu'une préparation ancienne n'écrase des ajouts intervenus entre-temps.

## Annexe D — comparaison des dépôts officiels

Les quatre dépôts ont été confiés chacun à un agent dédié pour un clone temporaire et une lecture statique. Le CLI Kuberpack a été lu séparément par un agent Luna Medium. Les observations comparatives et les références figées sont détaillées ci-dessous ; elles inspirent des choix de produit, sans supposer que Kuberpack possède les mêmes APIs.

### Révisions examinées

| CLI | Dépôt officiel et révision clonée | Portée |
| --- | --- | --- |
| Railway | [railwayapp/cli — b62467ef1b5303e3e340283ed96425dcf9612993](https://github.com/railwayapp/cli/tree/b62467ef1b5303e3e340283ed96425dcf9612993) | Commandes, contexte, authentification, déploiement, logs et confirmations. |
| Render | [render-oss/cli — 7129791ee688c7fb0622bef2f571298c1b682e9c](https://github.com/render-oss/cli/tree/7129791ee688c7fb0622bef2f571298c1b682e9c) | Client API/TUI, workspace, déploiement, sorties et séparation du dev local. |
| Coolify | [coollabsio/coolify-cli — e1d60947fc66c194d256b1049fd69c1ddf819760](https://github.com/coollabsio/coolify-cli/tree/e1d60947fc66c194d256b1049fd69c1ddf819760) | Contextes, apps/projets, déploiement, surface opérateur, secrets et retries. |
| Cloudflare Wrangler | [cloudflare/workers-sdk — 8a30d8eec16110dcaaeb0822768ac03a5b45a902](https://github.com/cloudflare/workers-sdk/tree/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler) | Clone sparse de Wrangler : commandes, authentification, versions, secrets et confirmations. |

Les clones restent dans `/tmp/kuberpack-cli-audit-20261025-{railway,render,coolify,cloudflare}`. Aucune dépendance des dépôts comparés n'a été installée, aucun de leurs CLI n'a été exécuté et aucun compte n'a été connecté. Les constats portent sur ces révisions, pas sur toutes leurs versions ou sur la sécurité de leurs serveurs.

### Railway : parcours court et association au projet

Railway fournit le parcours `login → init/link → up → status/logs`. `link` associe un dossier à un projet ; `up` archive et téléverse les sources vers le service distant. Le client n'exécute pas le control plane. **Choix retenu pour Kuberpack :** connexion, association locale et observation simples. **Différence assumée :** Kuberpack continue à construire des commits distants, sans upload des sources locales. Sources : [link](https://github.com/railwayapp/cli/blob/b62467ef1b5303e3e340283ed96425dcf9612993/src/commands/link.rs), [up](https://github.com/railwayapp/cli/blob/b62467ef1b5303e3e340283ed96425dcf9612993/src/commands/up.rs).

Railway distingue le retour immédiat de `--detach` du suivi du déploiement. En revanche, `deploy` désigne des templates et `up` les sources, ce qui rend les verbes moins prévisibles. Certaines voies détectant un agent acceptent aussi un consentement implicite. **Choix Kuberpack :** un verbe documenté pour chaque effet et aucune confirmation déduite d'une détection de CI ou d'agent. Sources : [up](https://github.com/railwayapp/cli/blob/b62467ef1b5303e3e340283ed96425dcf9612993/src/commands/up.rs), [contexte d'exécution](https://github.com/railwayapp/cli/blob/b62467ef1b5303e3e340283ed96425dcf9612993/src/exec_context.rs).

### Render : cible explicite et attente du résultat

Render organise l'utilisation autour du workspace et des services. `deploys create` demande un déploiement distant et `--wait` en suit le verdict. La suppression de service permet de consulter un aperçu avant confirmation. **Choix retenu :** montrer la cible et l'impact, puis suivre une demande identifiable jusqu'à son résultat. Sources : [workspace](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/cmd/workspaceset.go), [déploiement](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/cmd/deploycreate.go), [suppression](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/cmd/servicedelete.go).

Le wrapper générique de certaines mutations peut toutefois lire stdin et écrire un prompt sur stdout, même avec une sortie structurée. Le mode JSON des logs observé n'est pas un cadrage NDJSON exploitable tel quel. **Choix Kuberpack :** contrat non interactif explicite et séparation stricte entre résultat, flux et progression. Sources : [wrapper](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/pkg/command/wrapper.go), [logs](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/cmd/logs.go).

Render possède aussi un serveur local Workflows lié au développement, écoutant sur `localhost`. Ce composant a une fonction locale précise ; il ne lance pas le control plane Render. Cette nuance ne justifie aucune commande serveur dans le client Kuberpack. [Serveur Workflows](https://github.com/render-oss/cli/blob/7129791ee688c7fb0622bef2f571298c1b682e9c/pkg/workflows/apiserver/server.go).

### Coolify : plusieurs instances, avec une surface à réduire

Coolify associe URL et token dans des contextes nommés. Ses déploiements passent par l'API et renvoient un UUID. **Choix retenu :** contextes explicites et identifiant durable permettant d'observer sans répéter la mutation. Sources : [résolution du client](https://github.com/coollabsio/coolify-cli/blob/e1d60947fc66c194d256b1049fd69c1ddf819760/internal/cli/client.go), [déploiement](https://github.com/coollabsio/coolify-cli/blob/e1d60947fc66c194d256b1049fd69c1ddf819760/cmd/deployment/uuid.go).

Son arbre public comporte aussi des pouvoirs opérateur tels que `server`, `privatekeys` et `settings`, ainsi que des tokens acceptés dans argv. Certaines commandes internes expérimentales sont, elles, explicitement exclues de la racine. **Choix Kuberpack :** distribuer une surface développeur réduite et une distribution opérateur séparée ; prendre les secrets par saisie masquée ou stdin. [Commandes et options racine](https://github.com/coollabsio/coolify-cli/blob/e1d60947fc66c194d256b1049fd69c1ddf819760/cmd/root.go).

Le client HTTP observé retente aussi les POST sur erreurs réseau et 5xx sans filtrer par méthode. Cela constitue un risque de répétition déduit du client, sans démontrer une duplication côté serveur Coolify. **Choix Kuberpack :** toute reprise d'écriture exige une idempotence serveur. [Retries](https://github.com/coollabsio/coolify-cli/blob/e1d60947fc66c194d256b1049fd69c1ddf819760/internal/api/client.go#L174).

### Cloudflare Wrangler : séparer préparation, publication et observation

Wrangler distingue initialisation, serveur applicatif de développement, publication, versions et logs distants. `secret put` lit une valeur masquée ou stdin ; `whoami` expose le périmètre d'authentification. **Choix retenu :** verbes compréhensibles, introspection du jeton et séparation entre build, demande de déploiement et version active. Sources : [commandes](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/index.ts), [secrets](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/secret/index.ts), [whoami](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/user/whoami.ts).

Le helper de confirmation utilise un repli positif par défaut hors terminal, et certaines mutations l'emploient ainsi. **Choix Kuberpack :** une confirmation absente bloque la mutation. Le `dev` de Wrangler sert un Worker local et le callback OAuth a encore une autre fonction ; aucun de ces serveurs n'est un control plane à importer dans le client Kuberpack. Sources : [confirmations](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/dialogs.ts), [rollback](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/versions/rollback/index.ts), [dev](https://github.com/cloudflare/workers-sdk/blob/8a30d8eec16110dcaaeb0822768ac03a5b45a902/packages/wrangler/src/dev.ts).

### Synthèse des choix déduits de la comparaison

| Inspiration | Décision pour Kuberpack |
| --- | --- |
| Railway : association du dossier | `link` local, cible projet/service mémorisée sans toucher au dépôt. |
| Render : services et déploiements observables | `status`, `deployments wait` et actions adaptées au type de service. |
| Coolify : contextes et UUID de déploiement | URL/credential liés ; demande identifiée et reprise idempotente. |
| Wrangler : tâches et séparation version/activation | `init`, `deploy`, `builds`, `logs` avec effets précisément documentés. |
| Serveurs locaux utiles à d'autres produits | Aucune fonction équivalente requise dans le client Kuberpack. |
| Surface admin et confirmations permissives observées | Permissions serveur, artefacts distincts, refus par défaut et aucun secret en argv. |

## Annexe E — arbitrage sur l'assistant interactif

Deux agents ont confronté directement les approches : un accueil central orienté tâches et des commandes autonomes avec invites. Chacun a répondu aux objections de l'autre avant la synthèse. Le compromis retenu combine les deux accès avec les mêmes formulaires et règles d'exécution.

| Point discuté | Correction retenue |
| --- | --- |
| Un nouveau `wizard` suffirait-il à rendre le CLI agréable ? | Non : `login`, `init` et `link` doivent également fonctionner seuls, sans flags obligatoires en terminal. L'accueil sert à découvrir et enchaîner les tâches. |
| Faut-il refaire saisir les informations déjà connues ? | Non : présenter les données du contexte, du jeton et de Git dans un récapitulatif modifiable ; demander les seuls champs nécessaires manquants. |
| L'assistant doit-il tout confirmer à chaque étape ? | Non : distinguer l'enregistrement local de la publication distante et conserver une seule confirmation finale du plan pour publier. |
| Peut-on lier le premier service avant sa création ? | Non : associer le projet réservé, puis mémoriser le service après observation de son enregistrement par l'API, même si le build échoue ensuite. |
| Peut-on toujours générer un petit manifeste initial ? | Non : sur un projet déjà rempli, récupérer explicitement le manifeste complet ; éviter une suppression involontaire des autres services. |
| Les options explicites restent-elles utiles ? | Oui : elles préremplissent les formulaires et servent aux commandes reproductibles et aux pipelines. Leur liste complète n'est plus le premier parcours présenté au développeur. |
| Comment nommer l'entrée ? | `wizard`, accessible aussi par `kuberpack` sans argument en terminal ; aucune seconde orthographe à maintenir. |

## Critères de validation de la proposition

La revue peut se concentrer sur six choix : commandes courtes et accueil guidé ; séparation des distributions ; catalogue des commandes ; publication d'un manifeste complet avec `deploy` ; réservation initiale et destruction des projets par l'administrateur ; permissions et suivi nécessaires côté control plane.

Une implémentation ultérieure devra démontrer au minimum : impossibilité d'obtenir un listener ou un worker avec le binaire développeur ; refus API d'une suppression par un jeton de projet ; absence d'admin par omission ; blocage sur un volume persistant même sans addon ; impossibilité pour une ancienne génération saine de valider un nouveau déploiement ; reprise sans duplication après coupure ; aucune invite ou fuite de credential dans une sortie machine.

Le parcours interactif devra aussi fonctionner avec `kuberpack` seul ou `login → init → deploy`, sans flags ni `link` imposés pour le premier service. Il devra préserver un fichier existant, empêcher un brouillon destiné à un projet vide de remplacer un projet déjà peuplé et annoncer exactement les effets conservés après une annulation.

**L’audit initial était documentaire. La refonte est désormais implémentée dans le dépôt ; consulter [le guide livré](cli.md) pour son utilisation et les écarts de première version.**

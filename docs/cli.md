# Utiliser Kuberpack depuis le terminal

`kuberpack` est le client développeur de l'API v2. Il permet de préparer un projet, publier sa configuration, reconstruire ses services et consulter leur état. Le control plane et les workers sont des exécutables distincts ; le client ne contient pas de commande serveur, de builder ou de connexion Kubernetes.

## Démarrage guidé

L'administrateur réserve le projet, autorise ses dépôts sources et domaines, puis vous remet un jeton `maintainer`. Depuis votre dossier de travail :

```bash
kuberpack
```

Choisissez **Prepare and deploy this directory**. L'assistant demande l'URL HTTPS et le jeton seulement si la connexion manque. Il présente le projet autorisé, propose le dépôt Git `origin`, demande les renseignements manquants et prépare `kuberpack.yaml`. Le récapitulatif permet de modifier les choix ou d'ouvrir les réglages avancés : branche, port, healthcheck, ressources, PostgreSQL ou Valkey.

Il enregistre la configuration locale avant de préparer le plan distant. **Seule la confirmation du plan demande le déploiement.** Annuler ce plan conserve le fichier et l'association déjà enregistrés, sans build ou commit distant. Ctrl-C pendant le suivi arrête l'observation, sans annuler le travail accepté par le serveur.

Vous pouvez accéder directement à chaque étape, toujours sans liste de flags :

```bash
kuberpack login
kuberpack init
kuberpack deploy
```

Le projet doit déjà être réservé. Un premier service n'a pas besoin d'exister avant `init` : l'association initiale porte sur le projet seulement. Pour un projet déjà rempli, l'assistant propose `link` ou la récupération explicite du manifeste complet. Il ne remplace pas ses autres services par un fragment généré et ne réécrit pas un fichier local existant.

`kuberpack wizard` ouvre le même assistant. Les formulaires sont disponibles uniquement en terminal interactif. Hors terminal, avec `CI=true`, `--non-interactive` ou `--output json`, utilisez les commandes directes et fournissez les paramètres nécessaires. `--yes` accepte une action renseignée ; il ne choisit aucune réponse manquante. L'invocation sans argument affiche alors l'aide.

## Au quotidien

```bash
kuberpack status
kuberpack status --service web
kuberpack logs --follow
kuberpack builds list
kuberpack builds logs --latest --follow
kuberpack redeploy
kuberpack deployments list
kuberpack deployments wait dep_ID
```

La connexion et le projet sont mémorisés dans la configuration utilisateur. `link` associe un autre dossier à un projet existant et permet de sélectionner un service par défaut pour les logs, les builds et la reconstruction. `status` sans `--service` affiche le projet. Une cible ambiguë est demandée en terminal et refusée en automatisation.

Un push Git sur la branche configurée déclenche normalement un build. Les fichiers non poussés ne sont jamais téléversés par le CLI. `redeploy` reconstruit un service `repository:` depuis sa configuration enregistrée ; ce n'est pas un redémarrage des pods. Pour un service `image:`, `redeploy` re-pinne le digest du tag sans builder de build ; aucun build Kuberpack ne lui est associé.

`deploy` publie le **manifeste complet** du projet. Il refuse `--service`. La préparation du plan valide l'état distant, les permissions, les quotas, les paramètres opérateur, les références et la compilation dans un checkout temporaire sans push. Les commits et les digests affichés sont conservés dans le plan. `validate` effectue uniquement les contrôles structurels locaux, sans réseau.

```bash
kuberpack validate
kuberpack deploy --plan
kuberpack deploy --plan-id plan_ID --yes
kuberpack init --from-remote
```

La dernière commande récupère explicitement le manifeste distant complet ; elle refuse une destination déjà existante. `--file` permet de choisir un autre emplacement. Il n'est pas obligatoire de placer le manifeste dans le dépôt applicatif.

## Attente et erreurs

`deploy` et `redeploy` attendent la convergence par défaut. `--detach` retourne après acceptation et affiche un identifiant durable. `--timeout` borne l'attente locale ; l'expiration ou Ctrl-C conserve cet identifiant pour `deployments wait`.

Le suivi vérifie les builds liés à la demande, le digest attendu, la révision de configuration portée par le workload et sa génération réellement observée. Une ancienne version saine ne valide pas la nouvelle configuration. Les retraits de services attendent leur disparition. Un rollback ou une demande remplacée ne donne pas un succès.

Après un redémarrage du control plane, un build marqué en cours est signalé comme interrompu : le serveur ne recrée pas aveuglément un Job qui peut encore exister. Une interruption après un commit GitOps est conservée comme échec partiel avec son commit ; il faut inspecter ce travail avant de préparer une nouvelle demande. Les étapes et erreurs restent consultables par identifiant.

| Code | Sens |
| --- | --- |
| `0` | Commande réussie ; avec `--detach`, demande acceptée seulement. |
| `1` | Erreur réseau, serveur ou interne. |
| `2` | Syntaxe, configuration, ambiguïté, conflit ou confirmation manquante. |
| `3` | Authentification refusée. |
| `4` | Permission ou politique refusée. |
| `5` | Déploiement échoué, rollback ou demande remplacée. |
| `6` | Attente expirée. |
| `130` | Interruption locale. |

Une lecture `status` réussie retourne `0` même si l'application est en erreur. Pour contrôler la livraison dans un pipeline, attendez l'identifiant de déploiement.

## Credentials et automatisation

`login` vérifie le jeton via `/api/v2/session`. Le client développeur refuse les jetons administrateur ; ceux-ci appartiennent à `kuberpack-admin`. Les contextes associent une URL à un credential, sans réutiliser un secret pour une URL différente. HTTPS et la vérification des certificats sont obligatoires. `--ca-file` ajoute une CA privée ; aucune redirection authentifiée n'est suivie.

Sur Linux, le trousseau utilise `secret-tool`. S'il est absent ou indisponible, le mode fichier exige un choix explicite : invite interactive ou `--credential-store file`. Le fichier de credential est privé (`0600`, répertoire `0700`). Sur les autres systèmes, choisissez explicitement ce stockage. Les listes et sorties JSON n'incluent pas le secret. `logout` supprime le credential local sans révoquer le jeton serveur.

Pour une connexion sans invite :

```bash
# stdin reçoit le jeton depuis votre gestionnaire de secrets.
kuberpack login --url https://kuberpack.example.com \
  --token-stdin --credential-store file
```

Un runner injecte ensemble `KUBERPACK_API_URL` et `KUBERPACK_API_TOKEN`. Ce mode n'utilise ni ne persiste les credentials d'un contexte et refuse `--context`. Utilisez un jeton `deployer` limité au projet et, si nécessaire, au service.

```bash
kuberpack redeploy --project demo --service web \
  --commit "$CI_COMMIT_SHA" \
  --request-id "pipeline-$CI_PIPELINE_ID-web" \
  --non-interactive --yes --wait --output json
```

La première version exige que `--commit` soit le SHA complet du **HEAD vérifié** de la branche configurée. Elle ne propose pas encore la sélection d'un ancien commit par vérification d'ascendance sur chaque forge. Une branche ayant avancé provoque un refus explicite.

Réutiliser le même `--request-id` avec les mêmes entrées retrouve la demande précédente sans nouveau plan ni build. Des entrées différentes donnent un conflit. Sans identifiant fourni, le CLI en génère un et l'affiche sur stderr. Un résultat fini produit un document JSON ; les suivis de logs ou d'état produisent du NDJSON. Une erreur JSON contient `error.code` et `error.message` ; un déploiement échoué conserve aussi son résultat et son identifiant sous `result`. Les diagnostics et la progression sont sur stderr.

## Administration

L'opérateur utilise une session distincte avec `kuberpack-admin`. Pour le bootstrap, les credentials Git et registre sont fournis dans les variables dédiées ; l'éventuel jeton API initial vient de `KUBERPACK_BOOTSTRAP_API_TOKEN`. Le contexte Kubernetes est obligatoire et l'installation exige une confirmation explicite :

```bash
kuberpack-admin install --context mon-cluster --config plateforme.yaml --plan
kuberpack-admin install --context mon-cluster --config plateforme.yaml --yes
kuberpack-admin doctor network --context mon-cluster --detect-only
```

L'installation publie le serveur derrière l'Ingress prévu. Les secrets ne se passent plus dans les arguments et le NodePort est retiré. Les anciens fichiers de bootstrap administrateur ne sont pas importés comme connexions développeur.

Avec un credential API administrateur, réserver le projet puis créer un jeton :

```bash
kuberpack-admin projects create demo --yes
```

Une réservation neuve n'autorise aucun dépôt source ni domaine public. L'opérateur doit configurer la politique décrite ci-dessous avant le premier déploiement. Pour un projet historique sans politique enregistrée, seuls ses dépôts et hostnames déjà présents sont conservés comme périmètre initial.

Le rôle et l'expiration sont obligatoires. L'omission du projet ne crée jamais un administrateur. Pour un rôle `admin`, `--project` est interdit. Les rôles de projet sont `reader` (inventaire, états et logs), `deployer` (reconstruction) et `maintainer` (publication du manifeste). Un `deployer` peut être restreint par `--service web`, répétable. La nouvelle valeur secrète va uniquement vers une destination exclusive en `0600` ; l'écran affiche ses métadonnées.

`projects configure` accepte une politique YAML non secrète ; `--plan` l'affiche localement sans la publier. Exemple :

```yaml
namespace: demo
max_services: 10
max_replicas: 3
domains: [example.com]
repositories: [acme/api]
allow_root: false
allow_writable_root: false
```

```bash
kuberpack-admin projects configure demo --file politique.yaml --plan
kuberpack-admin projects configure demo --file politique.yaml --yes

# Remettre ensuite au développeur un credential limité au projet.
kuberpack-admin tokens create --name alice --project demo \
  --role maintainer --expires 720h --output-file ./alice.token --yes
```

L'identité Infisical, les exceptions de sécurité et les limites appartiennent à cette politique. Les changements de namespace ou d'identité nécessitent une migration explicite. Les retraits/remplacements d'addons et les modifications de volumes persistants sont bloqués dans le parcours développeur.

La destruction est réservée à l'administrateur, exige un plan et le nom exact, et bloque sur tout addon, PVC, volume persistant ou inventaire inaccessible. Il n'y a pas de `--force` :

```bash
kuberpack-admin projects destroy demo --plan
kuberpack-admin projects destroy demo --plan-id plan_ID --confirm-project demo --yes
```

Le namespace est conservé pour son nettoyage opérateur distinct. Un ancien projet dont le namespace appartient au GitOps global doit d'abord migrer vers l'isolation des tenants. Les jetons de projet sont révoqués à la destruction.

## Construire et migrer

```bash
make build
./bin/kuberpack --help
./bin/kuberpack-admin --help
```

Les cinq exécutables sont `kuberpack`, `kuberpack-admin`, `kuberpack-control-plane`, `kuberpack-builder` et `kuberpack-reloader`. Les trois derniers appartiennent aux workloads, pas au poste développeur. Les images, points d'entrée des charts et politiques d'admission des Jobs doivent être mis à jour ensemble.

Le nouveau client exige l'API v2 et ses capacités annoncées ; il ne se replie pas vers une écriture v1. Les anciennes écritures de projet et de redeploy répondent `410` ou `403`, même avec un ancien client. Les jetons historiques de rôle `project` restent utilisables en lecture et doivent être remplacés par des jetons explicites et expirants pour les nouvelles mutations.

Les documents d'audit datés et [la proposition validée](cli-proposal.md) conservent le raisonnement initial. Ce guide décrit l'interface livrée dans le dépôt ; il ne signifie pas qu'une instance existante a déjà été mise à niveau.

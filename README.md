# Kuberpack

PaaS GitOps : `git push` → image immuable → GitOps → Flux.

Le client développeur, l'administration, le control plane et les workers ont des exécutables distincts. Le poste développeur utilise uniquement l'API HTTPS ; il ne reçoit ni kubeconfig, ni credential GitOps ou de registre.

## Commencer

L'administrateur prépare votre projet et ses autorisations de dépôts/domaines, puis vous remet un jeton limité à ce projet. Depuis le dossier de l'application :

```bash
kuberpack
```

Choisissez **Prepare and deploy this directory**. L'assistant propose les informations disponibles, demande ce qui manque, prépare le manifeste puis affiche le plan à confirmer. Il est aussi accessible par `kuberpack wizard`.

Les commandes directes sont interactives, sans flags obligatoires :

```bash
kuberpack login
kuberpack init
kuberpack deploy

kuberpack status
kuberpack logs --follow
kuberpack builds logs --latest --follow
```

Les sources doivent être poussées dans Git. `deploy` publie le manifeste complet du projet ; `redeploy` reconstruit un service déjà configuré. Le CLI ne téléverse jamais les fichiers locaux et ne construit pas sur votre machine.

Voir le [guide CLI](docs/cli.md) pour les contextes, les scripts, les permissions, l'administration et la migration.

## Installer la plateforme

Un Kubernetes existant, une forge Git, un registre OCI et deux dépôts GitOps distincts sont nécessaires. Les builds utilisent BuildKit rootless ; les images sont épinglées par digest. Les secrets restent hors des manifests et des valeurs SQLite opérationnelles.

Avec les credentials Git/registre dans les variables dédiées et un contexte Kubernetes explicite :

```bash
kuberpack-admin install --context mon-cluster --config plateforme.yaml --plan
kuberpack-admin install --context mon-cluster --config plateforme.yaml --yes
```

L'installation prépare Flux, les dépendances, Infisical et le control plane derrière un Ingress. Les prérequis et choix de configuration sont dans [docs/install.md](docs/install.md). Elle ne nécessite pas de commande `serve` sur le poste développeur.

## Développer Kuberpack

```bash
make test-static
make test
make test-heavy
make test-superheavy
make build
./bin/kuberpack --help
./bin/kuberpack-admin --help
```

`make build` produit les cinq exécutables. `Dockerfile` embarque `kuberpack-control-plane` et `kuberpack-reloader` ; `Dockerfile.builder` embarque `kuberpack-builder`. Les images, les charts et les règles d'admission des Jobs doivent évoluer ensemble.

Les tests utilisent Docker et Python 3 sur l'hôte. Leurs outils et fixtures sont fournis par les mêmes conteneurs qu'en CI ; [test/](test/README.md) décrit les niveaux et les rapports.

L'API v2 apporte les rôles `reader`, `deployer`, `maintainer` et `admin`, les plans expirants, les demandes persistantes et leur idempotence. Les écritures historiques v1 sont retirées ; les lectures de diagnostic v1 restent disponibles. La mise à niveau d'une instance et le remplacement de ses anciens jetons restent des opérations d'administration explicites.

## Documentation

| Document | Contenu |
| --- | --- |
| [Guide CLI](docs/cli.md) | Assistant, commandes, authentification, automatisation et migration |
| [API](docs/api.md) | API v2, plans, demandes et lectures de diagnostic |
| [Installation](docs/install.md) | Préparer et installer la plateforme |
| [Manifeste](docs/manifest.md) | Configuration complète d'un projet |
| [Architecture](docs/architecture.md) | Plans, sources de vérité et GitOps |
| [Codebase](docs/codebase.md) | Responsabilités et trajets dans le code |
| [Sécurité](docs/security.md) | Credentials, isolation et frontières de confiance |
| [Builder](docs/builder.md) | Stratégies, scans et images |
| [Isolation des builds](docs/build-isolation.md) | RuntimeClass, pool dédié et limites |
| [Tests](docs/testing.md) | Vérifications locales et bancs d'intégration |
| [État du code](docs/status.md) | Implémentation et limites |
| [Proposition et audit CLI](docs/cli-proposal.md) | Décisions validées et audit initial daté |

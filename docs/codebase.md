> La CLI est maintenant séparée : `internal/cli` (client et assistant), `internal/api`/`internal/spec` (contrat sans Kubernetes), `internal/platformcli` (installation), `internal/runtimecli` (serveur/reloader) et points d’entrée dédiés sous `cmd/`. Le builder appelle directement `internal/buildexec`. Voir [le guide CLI](cli.md) et [l’API v2](api.md) ; les descriptions de l’ancien dispatcher plus bas sont conservées comme contexte architectural.

# Carte de la codebase

Ce document est destiné à un développeur qui arrive dans le dépôt. Il explique
où chaque responsabilité vit, pourquoi `provisioning/` contient aussi du Go, et
comment les morceaux s'enchaînent. Le contrat produit reste
[architecture.md](architecture.md) ; ici, on parle du code.

## La vue en trente secondes

Kuberpack a **trois plans** et **deux boucles** :

```text
                    ┌──────────────────────────────┐
   git push ───────▶│ 1. control plane (internal/   │
   webhook           │    control)                  │
                    └──────────────┬───────────────┘
                                   │ Job Kubernetes
                                   ▼
                    ┌──────────────────────────────┐
                    │ 2. build (internal/builder,  │
                    │    internal/buildexec)       │
                    │    clone → build → scan      │
                    │    → push image + digest     │
                    └──────────────┬───────────────┘
                                   │ digest
                                   ▼
                    ┌──────────────────────────────┐
                    │ 3. GitOps (internal/promote) │
                    │    écrit le dépôt apps       │
                    └──────────────┬───────────────┘
                                   │ Flux
                                   ▼
                    ┌──────────────────────────────┐
                    │ cluster de production        │
                    │ provisioning/charts/stateless │
                    └──────────────────────────────┘
```

Deux dépôts Git sont utilisés :

- **le dépôt du socle** : installation, chart `kuberpack`, Kustomizations
  Flux. Il est possédé par l'installateur ; le control plane n'y écrit pas.
- **le dépôt des applications** : `kuberpack.yaml`, valeurs générées,
  HelmRelease, secrets Infisical. Le control plane l'écrit.

Deux charts Helm sont livrés :

- `provisioning/charts/kuberpack` : le socle (control plane, RBAC, admission, reloader) ;
- `provisioning/charts/stateless` : une application HTTP (Deployment, Service, Ingress,
  NetworkPolicy, test Helm).

## Arborescence

```text
cmd/kuberpack/        client développeur API (internal/cli)
cmd/kuberpack-admin/  administration API et bootstrap
internal/runtimecli/ control plane et reloader
internal/platformcli/ installation et diagnostics opérateur
internal/             logique métier, packages importables
provisioning/         charts Helm embarqués + tests Go des charts
test/                 suites Docker locales/CI, fixtures et playbooks Ansible
examples/             manifestes d'exemple
docs/                 contrat, exploitation, audits
Dockerfile            image du control plane (sans shell)
Dockerfile.builder    image du builder (Railpack, BuildKit, Trivy, Syft)
```

### `cmd/kuberpack/` : la composition

C'est le seul package `main`. Il ne contient pas la logique métier : il relie
les flags, les clients Kubernetes/Git et les packages `internal/`.

| Fichier | Rôle |
| --- | --- |
| `main.go` | dispatch des commandes (`install`, `serve`, `project`, `app`, `doctor`, `reloader`, `builder`…) |
| `serve.go` | construit le `control.Config` et lance le serveur HTTP |
| `install.go`, `install_*.go` | parcours d'installation, découpé par étape (Flux, Helm, Infisical, GitOps, vérification) |
| `project.go` | `project init/compile/create/delete` côté CLI |
| `app.go`, `applogs.go`, `appresources.go` | lecture et pilotage d'une application via l'API |
| `token.go` | gestion des jetons API de projet |
| `doctor.go` | diagnostic réseau |
| `reloader.go` | processus qui redémarre les pods après rotation de secret |
| `flux.go`, `clusterservices.go`, `infisical*.go` | helpers d'installation |

Le découpage `install_*.go` est volontairement linéaire : chaque fichier
correspond à une étape ou à une famille d'objets. `internal/install` ne contient
que le **modèle de configuration** et le **playbook** (liste d'étapes), pas les
étapes elles-mêmes. C'est une frontière perfectible : déplacer les étapes dans
`internal/install` rendrait le package `main` plus mince.

### `internal/` : les responsabilités

| Package | Responsabilité |
| --- | --- |
| `promote` | **compilateur** : lit `kuberpack.yaml`, rend l'arbre GitOps (values, HelmRelease, addons, NetworkPolicy, InfisicalStaticSecret, Kustomization) |
| `control` | API HTTP, webhooks, réconciliation périodique, provisioning des tenants, jetons API, logs/status |
| `release` | orchestration d'une livraison : clone, build, scan, push, attente du digest, pin GitOps |
| `builder` | création du **Job Kubernetes** qui exécute le builder |
| `buildexec` | code exécuté **dans** le Job builder : clone, Railpack, BuildKit, Trivy, Syft, push |
| `buildsarchive` | archive optionnelle des logs/SBOM dans un dépôt Git |
| `store` | SQLite : apps, builds, previews, jetons API ; secrets HMAC hors SQLite |
| `git` | abstraction des forges : Forgejo, GitHub, GitLab |
| `forgejo` | client bas niveau de l'API Forgejo/Gitea |
| `fetch` | clone Git sécurisé (origine, auth, symlinks) |
| `oci`, `image` | registre OCI, digests, références d'images |
| `hmacsig` | signature des webhooks |
| `netguard` | garde SSRF des accès sortants |
| `netcheck` | détection du CNI et probe réelle des NetworkPolicy |
| `reloader` | redémarrage des workloads quand un Secret synchronisé change |
| `railpack`, `strategy` | détection de la stratégie de build |
| `fail` | étapes et messages d'échec lisibles |
| `install` | configuration non secrète et playbook d'installation |

Règle de dépendance : `cmd` dépend de `internal` ; `internal/control` dépend de
`promote`, `store`, `release`, `builder`, etc. Aucun package `internal` ne doit
importer `cmd`.

### `provisioning/` : le domaine, et `charts/` l'artefact

Le dossier est nommé par sa **finalité** — provisionner la plateforme et les
applications — et non par le seul format de ses fichiers. Les charts Helm sont
un sous-dossier :

```text
provisioning/
  embed.go                  package provisioning ; //go:embed charts/...
  charts/
    kuberpack/              chart du socle
    stateless/              chart d'une application
  *_test.go                 tests Go qui rendent/valident les charts
```

`provisioning/embed.go` :

```go
//go:embed all:charts/kuberpack all:charts/stateless
var Files embed.FS
```

Conséquence : le CLI et l'image du control plane **embarquent** les charts. Un
`kuberpack install` n'a pas besoin d'un checkout du dépôt ni de Helmfile. Les
fichiers `provisioning/*_test.go` sont des tests, pas du code runtime : ils vivent là
parce qu'ils chargent les charts avec `loader.Load("charts/kuberpack")` et vérifient le
rendu, le RBAC ou l'admission sur un vrai serveur API.

#### `provisioning/charts/kuberpack` : le socle

C'est le chart qui installe le control plane et son environnement immédiat.

| Template | Contenu |
| --- | --- |
| `deployment.yaml` | Deployment du control plane, env, probes, sécurité du pod |
| `service.yaml` | Service et Ingress de l'API |
| `rbac.yaml` | ServiceAccounts, dispatch du builder, rôles du provisionneur, reloader, métriques |
| `build-admission.yaml` | ValidatingAdmissionPolicy des Jobs/Pods du builder |
| `tenant-admission.yaml` | policies qui bornent ce que le control plane peut créer (namespaces, SA, RoleBindings, Kustomizations) |
| `reloader.yaml` | Deployment du reloader de secrets |
| `networkpolicy.yaml`, `controlplane-networkpolicy.yaml` | isolation réseau du builder et, en option, du control plane |
| `pvc.yaml`, `quota.yaml` | cache du builder, quotas du namespace de build |
| `namespace.yaml` | namespace du control plane |

Ce chart est **possédé par l'installateur**. En mode deux dépôts, le control
plane ne l'écrit pas (`KUBERPACK_WRITE_CHARTS=false`). C'est pour cela qu'un
changement de template doit être accompagné d'un **bump de version** dans
`provisioning/charts/kuberpack/Chart.yaml` : le HelmRelease utilise
`reconcileStrategy: ChartVersion`, et Flux ne réapplique pas un chart dont la
version n'a pas changé.

#### `provisioning/charts/stateless` : une application

C'est le chart utilisé par chaque `HelmRelease` applicative. Il ne connaît pas
Kuberpack : il reçoit des valeurs générées par `internal/promote`.

| Template | Contenu |
| --- | --- |
| `deployment.yaml` | Deployment, probes, ressources, sécurité, annotations |
| `service.yaml` | Service HTTP |
| `ingress.yaml` | Ingress et TLS |
| `networkpolicy.yaml` | ingress/egress du service, voisins, Internet, addons |
| `tests/http.yaml` | test Helm qui vérifie que l'application répond |
| `_helpers.tpl`, `values.yaml` | helpers et contrat de valeurs |

Le compilateur génère un `values.yaml` par service ; l'image et le `track` y
sont les seuls champs que le control plane réécrit. Le chart reste dans le
**dépôt du socle**, pas dans le dépôt applicatif : c'est ce qui empêche un
control plane compromis de modifier le chart qu'il fait appliquer.

#### Cycle de vie des charts

```text
provisioning/charts/ (dans le binaire)
   │ go:embed
   ▼
kuberpack install
   │ copie dans le dépôt du socle : <root>/charts/kuberpack, <root>/charts/stateless
   ▼
Flux HelmRelease "kuberpack"  ──▶ chart kuberpack (socle)
Flux HelmRelease "<service>"  ──▶ chart stateless (application)
```

`provisioning/charts/kuberpack` est appliqué par la Kustomization
`kuberpack-platform`. `provisioning/charts/stateless` est référencé par chaque
HelmRelease applicative via
`gitopsChart` (par défaut `./<root>/charts/stateless`). Le control plane ne
réécrit plus ces fichiers en mode deux dépôts.

### Le trajet d'une compilation

```text
kuberpack deploy --file kuberpack.yaml
  → POST /api/v1/projects
  → internal/control.createProject
      → internal/promote.EnsureProject
          clone du dépôt apps
          écrit projects/<projet>/…
          commit + push
      → provisionne namespace, ServiceAccount, Kustomization de tenant
      → reconcileApps : SQLite, webhook, premier build
```

`internal/promote` est le seul endroit qui connaît le format du manifeste et
l'arbre GitOps. Si tu ajoutes un champ au manifeste, commence par
`ProjectManifest`, `validateManifestFields`, puis le rendu concerné.

### Le trajet d'un build

```text
git push
  → webhook HMAC
  → internal/control
  → internal/release.Run
      → internal/builder.Job (Job Kubernetes)
          → internal/buildexec (dans le Job)
              clone → Railpack/BuildKit → Trivy → Syft → push
      → attente du digest
      → internal/promote.Run (pin dans le dépôt apps)
  → Flux réconcilie
```

### Le trajet d'une installation

```text
kuberpack install
  → internal/platformcli/install_plan.go (étapes)
  → internal/install.Config (choix non secrets)
  → clone du dépôt socle + du dépôt apps
  → Flux, opérateurs, Infisical, secrets
  → écriture du socle et remise à Flux
  → vérification API/releases
```

Les étapes sont reprises à chaque exécution : une installation interrompue se
relance avec la même commande.

## Où changer quoi

| Besoin | Endroit |
| --- | --- |
| Nouveau champ du manifeste | `internal/promote/project.go`, `manifest_validation.go`, rendu concerné, `docs/manifest.md` |
| Nouveau moteur d'addon | `internal/promote/addon_*.go`, registre dans `addon.go`, tests |
| Nouvelle route API | `internal/control/server.go` (routage) + handler, `docs/api.md` |
| Nouveau comportement de build | `internal/buildexec`, `internal/builder` |
| Nouvelle forge | `internal/git/` + `internal/forgejo/` si l'API est Gitea |
| Nouveau réglage d'installation | `internal/install/config.go`, `internal/platformcli/install_options.go`, values du chart |
| Nouveau template Kubernetes | `provisioning/charts/kuberpack` (socle) ou `provisioning/charts/stateless` (application), + bump de version |
| Nouveau test de chart | `provisioning/*_test.go` |
| Nouvelle règle réseau | `internal/promote/*networkpolicy*`, `provisioning/charts/stateless/templates/networkpolicy.yaml` |
| Nouvelle règle de sécurité | `docs/security.md`, `provisioning/charts/kuberpack/templates/*admission*`, tests |

## Conventions à garder

- **Un seul compilateur** : `internal/promote` produit l'arbre GitOps ; les
  handlers HTTP ne génèrent pas de YAML à la main.
- **Le dépôt du socle n'est pas écrit par le control plane.** Si tu ajoutes une
  écriture GitOps, demande-toi dans quel dépôt elle doit aller.
- **Les charts sont des artefacts versionnés.** Un changement de template sans
  bump de `Chart.yaml` ne sera pas appliqué par Flux.
- **Les fichiers générés ne sont pas édités à la main** : `values.yaml`,
  `helmrelease.yaml`, `kustomization.yaml` d'un projet sont réécrits à chaque
  compilation.
- **Les commentaires sont en français**, comme le reste du dépôt.
- **Un package, une responsabilité** : si un fichier de `cmd/` commence à
  contenir de la logique métier, elle appartient à `internal/`.

## Ce qui reste à améliorer dans l'organisation

- Les étapes d'installation sont dans `internal/platformcli/install_*.go` alors que
  `internal/install` ne porte que la configuration et le playbook. Les déplacer
  rendrait le point d'entrée plus lisible.
- Les tests de charts sont à la racine de `provisioning/` ; un sous-dossier
  `provisioning/tests/` serait plus explicite, mais le package Go doit rester
  importable par le CLI.
- `internal/promote/project.go` est le plus gros fichier du dépôt (compilateur
  complet). Un découpage par domaine (services, addons, secrets, réseau) serait
  possible sans changer le contrat.

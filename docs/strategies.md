# Stratégies de build — enseignements de Kubero

Cette note s'appuie sur les clones locaux de Kubero (`36a5046`) et de `kubero-operator` (`ea8327a`). Elle décrit ce que Kuberpack peut reprendre de leur organisation, sans reprendre leur mode de déploiement.

## Fonctionnement observé dans Kubero

Kubero sépare `deploymentstrategy` (`git` ou `docker`) de `buildstrategy` (`plain`, `dockerfile`, `nixpacks`, `buildpacks`). L'utilisateur choisit la stratégie dans la pipeline ; le formulaire d'application reprend cette valeur par défaut et permet de la changer. Un build manuel peut également choisir sa stratégie et sa référence Git. Kubero ne détecte pas automatiquement la stratégie à partir des fichiers du dépôt.

Pour les stratégies produisant une image, le serveur charge un modèle de Job Kubernetes selon `buildstrategy`. Les Jobs partagent le clonage du dépôt et un volume de travail. Les étapes suivantes changent :

| Stratégie | Construction |
| --- | --- |
| `plain` | Clone et build dans les init containers de chaque pod applicatif ; aucune image de release n'est produite. |
| `dockerfile` | Buildah construit et pousse le Dockerfile du dépôt. |
| `nixpacks` | Nixpacks génère un Dockerfile ; Buildah construit et pousse l'image. |
| `buildpacks` | Le cycle Cloud Native Buildpacks construit et pousse l'image. |

Les Jobs portent des labels pour retrouver l'application, la stratégie et les logs. Une fois l'image publiée, la dernière étape modifie directement `KuberoApp.spec.image` avec un tag.

Repères dans les sources locales :

- `kubero/client/src/components/pipelines/form.vue` et `apps/form.vue` : sélection de la stratégie ;
- `kubero/server/src/apps/apps.service.ts` : déclenchement du build ;
- `kubero/server/src/kubernetes/kubernetes.service.ts` : sélection et paramétrage du Job ;
- `kubero/server/src/deployments/templates/` et `kubero-operator/helm-charts/kuberobuild/templates/` : recettes par stratégie ;
- `kubero/server/src/deployments/deployments.service.ts` : suivi des Jobs.

## Contrat proposé pour Kuberpack

La configuration de l'application contient `strategy: auto | railpack | uv`. Un choix explicite a priorité. En mode `auto`, le builder inspecte **le commit exact** après le clonage :

1. `pyproject.toml` et `uv.lock` dans `root_directory` → `uv` ;
2. sinon → `railpack`, qui vérifie qu'il sait préparer le dépôt ;
3. si aucune stratégie ne convient → erreur explicite, sans promotion.

Une stratégie explicite `uv` échoue si les fichiers requis manquent ou si le lockfile est incohérent. Elle ne bascule pas silencieusement sur Railpack. La stratégie choisie et sa version sont conservées dans l'enregistrement du build. Une application non Python doit aussi valider le chemin Railpack du MVP.

```text
BuildRequest
  application, dépôt, commit_sha, root_directory
  stratégie demandée, overrides, image cible, build_id

Fetcher.Checkout(repository, commit_sha) -> SourceSnapshot

Strategy.Resolve(SourceSnapshot, config) -> BuildPlan
  stratégie résolue, version, recette de construction

Executor.Run(SourceSnapshot, BuildPlan) -> BuildResult
  commit_sha, image, digest, résultat du scan, référence SBOM
  état et logs par étape, erreur éventuelle
```

Le pipeline commun assure `fetch → plan → build → scan → publish`. Le fetcher vérifie le commit obtenu. Chaque stratégie fournit seulement sa recette de construction ; l'exécuteur assure les étapes restantes. Le composant de promotion vérifie ensuite que le SHA est toujours courant et écrit le digest dans le dépôt GitOps. Ainsi, ajouter une stratégie ne change ni le webhook, ni le scan, ni la promotion.

## Écarts à corriger par rapport à Kubero

- Le rebuild automatique de Kubero passe la branche enregistrée dans l'application au fetcher. Kuberpack transmet le SHA exact du webhook jusqu'au builder.
- Kubero insère des valeurs dans ses modèles de Job par indices (`initContainers[2]`, `env[1]`). Kuberpack utilise des champs nommés ou une structure validée.
- Les chemins Nixpacks et Dockerfile de Kubero utilisent Buildah privilégié. Kuberpack garde BuildKit rootless sur le k3s de build.
- Kubero termine en patchant une `KuberoApp` avec un tag. Kuberpack récupère un digest et laisse Flux déployer depuis Git.
- L'interface de logs Kubero contient des conditions par nom de stratégie. Kuberpack expose des étapes communes pour éviter de modifier l'interface à chaque nouvelle stratégie.
- Le code Gitea de Kubero calcule le HMAC sur un JSON reformaté. Kuberpack vérifie le corps HTTP brut avant de le parser.

Les sources analysées sont plus récentes que l'image Kubero `v1.11` utilisée pendant le premier essai. Cette note décrit leur structure locale, pas nécessairement tous les détails de cette ancienne image.

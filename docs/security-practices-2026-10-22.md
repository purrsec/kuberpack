# Revue des pratiques Kubernetes et d’Infisical — 22 octobre 2026

Cette revue couvre les manifests et le code d’installation, puis les recommandations officielles adaptées à Kuberpack. Elle ne constitue pas une certification de sécurité. Les contrôles sur un cluster doivent compléter les tests et les rendus de charts décrits ici.

## Infisical : constats et corrections

Le chart `secrets-operator 0.11.13` était installé avec ses valeurs par défaut. Son manager disposait d’un ClusterRole permettant notamment de lire et modifier les Secrets, modifier les workloads et créer des jetons de ServiceAccount dans tous les namespaces. Un operator compromis aurait pu demander un jeton d’un SA privilégié. [Règles officielles de cette version](https://github.com/Infisical/kubernetes-operator/blob/infisical-k8-operator/v0.11.13/helm-charts/secrets-operator/templates/manager-rbac.yaml).

L’installation gérée active maintenant `scopedRBAC: true` et une liste non vide `scopedNamespaces`. Elle contient `infisical` et les namespaces exacts des projets déclarés, y compris les noms personnalisés. Les namespaces système, Flux, du control plane et du builder sont refusés comme extensions du scope. Les projets inexistants ne sont pas déduits d’une découverte globale du cluster.

La liste est conservée dans `ConfigMap/kuberpack-infisical-operator-scope`, dans le namespace du control plane, avec une clé `values.yaml`. Le compilateur met à jour son manifeste dans le même commit que la création ou suppression d’un projet. Le HelmRelease operator référence cette ConfigMap via `valuesFrom`, sans répéter les valeurs du scope dans `spec.values` : celles-ci auraient priorité et figeraient la liste. [Priorité des valeurs Flux](https://fluxcd.io/flux/components/helm/helmreleases/#values-references).

L’operator lit `--namespaces` au démarrage. Une modification de la liste entraîne donc une mise à jour Helm et un redémarrage de son Deployment. Ajouter seulement des Roles sans étendre la liste de surveillance ne fonctionne pas : le cache de cette version refuse les lectures des Secrets cibles hors des namespaces configurés. Sur un cluster neuf, l’installation crée ces namespaces avant les Roles de l’operator. [Code du cache](https://github.com/Infisical/kubernetes-operator/blob/infisical-k8-operator/v0.11.13/cmd/main.go#L231), [installation scoped officielle](https://infisical.com/docs/integrations/platforms/kubernetes/overview).

Le serveur web Infisical et son Job de bootstrap partagent un SA dans le chart `infisical-standalone 1.11.1`. Le Deployment web reçoit désormais `automountServiceAccountToken: false`. Le Job conserve son jeton pour déposer les credentials de bootstrap, notamment après une installation interrompue. L’installation initiale et le post-renderer Flux appliquent le même réglage au Deployment uniquement. Le rôle de bootstrap reste présent pour la reprise ; l’identité n’est donc pas encore séparée en deux SA.

Les dépendances PostgreSQL et Redis ont explicitement l’automount désactivé. Leurs NetworkPolicies refusent les accès externes génériques et autorisent le processus web Infisical identifié par namespace et labels, en conservant les règles de réplication des charts. Les installations Helm externes réutilisées ne sont pas adoptées ni modifiées ; leur durcissement relève de leur propriétaire.

### Risques qui restent ouverts

- L’operator partagé peut encore lire les secrets et modifier les workloads de tous les projets gérés. Les rôles scoped du chart restent plus larges que les seules synchronisations utilisées. Un operator/une identité par projet réduirait cette portée.
- Les projets Kuberpack partagent actuellement une identité Infisical de lecture et un projet Infisical. Il faut des restrictions par chemin/environnement, ou des identités distinctes, pour empêcher une synchronisation compromise de lire les secrets d’un autre projet.
- Le namespace `infisical` contient encore les credentials de bootstrap et de l’instance, accessibles à l’operator dans ce scope. Séparer les namespaces de l’operator/authentification et du serveur/bootstrap reste pertinent.
- Sur un cluster partagé, le provisionnement doit aussi refuser les namespaces préexistants d’autres propriétaires. Les exclusions connues ne recensent pas automatiquement un Flux installé dans un namespace personnalisé ou tous les operators externes. Ajouter un tel namespace au scope pourrait exposer leurs Secrets ou permettre un TokenRequest sur leurs SA privilégiés.
- Le chart conserve une autorisation cluster TokenReview/SubjectAccessReview pour ses métriques. Cela ne rend pas les règles manager cluster-wide ; ces droits spécifiques doivent néanmoins être conservés seulement si nécessaires. Le template de cette version utilise encore le paramètre déprécié `scopedNamespace` pour ce RBAC métriques.

## Autorité GitOps et admission

Un CP ayant un RBAC Kubernetes limité mais le droit de pousser dans le dépôt qui pilote la plateforme conserve une autorité indirecte élevée. Un attaquant qui contrôle son token Git peut modifier les manifestes consommés par Flux. Les vérifications de chemins dans le processus compromis ne forment pas une frontière de sécurité.

La cible recommandée sépare le dépôt de plateforme, inaccessible en écriture au CP, du dépôt des applications. Les Kustomizations et HelmReleases des applications doivent réconcilier avec des SA limités à leurs namespaces. Le provisionnement des namespaces, des RBAC et des composants de plateforme relève d’une identité distincte. [Isolation multi-tenant Flux](https://fluxcd.io/flux/installation/configuration/multitenancy/), [RBAC des Kustomizations](https://fluxcd.io/flux/components/kustomize/kustomizations/#role-based-access-control).

Le droit de créer un Job autorise à définir son PodSpec. Il faut donc compléter le RBAC du dispatcher par une admission qui interdit notamment les workloads privilégiés, hostPath, hostNetwork/PID/IPC, les SA non prévus et les images non autorisées. Le namespace builder nécessite des exceptions précises pour BuildKit ; un simple label Pod Security `privileged` ne les borne pas. [Bonnes pratiques RBAC Kubernetes](https://kubernetes.io/docs/concepts/security/rbac-good-practices/).

Rootless BuildKit utilise ici `--oci-worker-no-process-sandbox` avec seccomp/AppArmor non confinés. L’amont indique que ce mode permet aux opérations de build de tuer, voire tracer selon la configuration, les processus du daemon. Pour exécuter du code non fiable : credentials limités distincts du CP, nœuds dédiés ou runtime sandbox, quotas et absence de jeton SA. [Limites rootless BuildKit](https://github.com/moby/buildkit/blob/master/docs/rootless.md).

## Réseau et portabilité

Une policy deny-all de namespace protège les pods auxquels aucune autorisation spécifique ne s’applique ; les accès DNS, ingress et voisins doivent être explicitement ajoutés. Les NetworkPolicies sont additives : une policy permissive provenant d’un autre workflow peut rouvrir un accès. Un dossier Git ne constitue pas une frontière réseau.

`internet: true` peut autoriser l’accès au domaine public d’une application d’un autre projet via l’ingress. L’isolation du réseau privé ne promet donc pas l’inaccessibilité de cette interface publique. Les policies natives ne bloquent pas tous les accès au nœud résident ou à localhost, et les effets des règles CIDR après traduction d’adresses dépendent du réseau. Des probes réelles d’accès permis et refusé sont nécessaires. [Modèle et limites Kubernetes](https://kubernetes.io/docs/concepts/services-networking/network-policies/).

- k3s embarque le moteur de NetworkPolicy kube-router sauf désactivation explicite. [Documentation k3s](https://docs.k3s.io/networking/networking-services#network-policy-controller).
- EKS nécessite l’activation de la fonctionnalité dans le VPC CNI. Le mode standard autorise le trafic durant une fenêtre initiale avant programmation ; le mode strict commence par refuser les accès. [Documentation AWS](https://docs.aws.amazon.com/eks/latest/userguide/cni-network-policy-configure.html).
- GKE applique les policies via Dataplane V2 ou Calico. Sans moteur activé, leur présence dans l’API ne prouve pas leur application. [Documentation Google](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/network-policy).

## Validation de ces changements

Tests Go des scopes et du bootstrap : ajout, suppression, déduplication, namespace personnalisé, refus des namespaces protégés et des symlinks, stabilité d’une réinstallation, conservation des métadonnées Flux. Un test Git réel vérifie que compilation et suppression publient la nouvelle liste dans le dépôt.

Le test réseau opt-in `TestInfisicalChartsEnforceScopeAndRuntimeHardening` télécharge et rend les deux versions officielles épinglées. Il vérifie l’absence du manager ClusterRole, les Roles dans les namespaces attendus, le Deployment web sans jeton, le Job de bootstrap préservé et les StatefulSets de données sans jeton. Le post-renderer Flux est également exécuté par Kustomize sur des ressources de contrôle pour vérifier qu’il ne modifie pas les Jobs ni les autres Deployments.

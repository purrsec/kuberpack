# Isoler les builds : RuntimeClass et pool dédié

Le builder exécute du code non fiable : le dépôt cloné, ses dépendances et les
commandes de build. Par défaut, ce code tourne dans un Pod du namespace
`kuberpack-build`, avec BuildKit rootless, mais sur le **noyau du cluster**.
L’admission `build-admission` borne les privilèges, les volumes et les images ;
elle ne protège pas contre une faille du noyau Linux ou de BuildKit.

Kuberpack propose deux réglages **optionnels** pour réduire ce risque :

- un `RuntimeClass` (gVisor, Kata) qui donne une sandbox au Pod du build ;
- un pool de nœuds dédié, avec `nodeSelector` et `tolerations`.

Ils ne sont **pas activés par défaut**. Une installation existante qui ne
fournit aucune de ces valeurs garde exactement le comportement historique :
runtime par défaut, nœud amd64 quelconque, aucune tolérance.

## Ce que cela protège

| Réglage | Effet | Limite |
| --- | --- | --- |
| `runtimeClass: gvisor` ou `kata` | Les appels système du builder et de BuildKit passent par un noyau ou une VM intermédiaire. Une faille du noyau hôte n’est plus directement atteignable depuis le build. | Dépend du runtime installé sur les nœuds. gVisor ne couvre pas tous les appels système ; BuildKit rootless reste une exception connue. |
| `nodeSelector` | Les builds ne démarrent que sur les nœuds qui portent les labels du pool. | Ne protège pas le noyau si aucun RuntimeClass n’est fourni. |
| `tolerations` | Les builds peuvent occuper un pool tainté, sans y attirer les autres charges. | Une tolérance universelle serait un contournement : elle est refusée. |

Le pool dédié borne aussi la contention CPU, mémoire et disque, et limite la
portée d’une compromission au nœud de build. Il ne remplace pas une frontière
multi-tenant complète : les credentials, le réseau et les failles de BuildKit
restent des surfaces distinctes. Sans RuntimeClass, le noyau reste partagé.

L’admission du builder épingle le `runtimeClassName` et le `nodeSelector`
configurés. Un control plane compromis ne peut donc pas replacer les builds sur
les nœuds du control plane ni changer de runtime. Les tolérances sont bornées :
clé obligatoire, opérateurs et effets connus, pas de tolérance qui accepte tous
les taints.

## Préparer le cluster

L’installateur Kuberpack ne crée ni pool de nœuds, ni taint, ni RuntimeClass.
Ces objets appartiennent à l’opérateur du cluster.

1. Installer le runtime sur les nœuds de build (par exemple gVisor avec le
   handler `runsc` de containerd, ou Kata). Vérifier qu’il est annoncé :

   ```bash
   kubectl get runtimeclass
   ```

2. Créer le RuntimeClass :

   ```yaml
   apiVersion: node.k8s.io/v1
   kind: RuntimeClass
   metadata:
     name: gvisor
   handler: runsc
   ```

   ```bash
   kubectl apply -f runtimeclass-gvisor.yaml
   ```

3. Réserver les nœuds du pool avec un label et un taint :

   ```bash
   kubectl label node build-1 kuberpack.dev/build=true
   kubectl taint node build-1 dedicated=build:NoSchedule
   ```

   Les nœuds doivent rester `linux/amd64` : le Job ajoute
   `kubernetes.io/arch=amd64`, sauf si ce label est remplacé dans le
   `nodeSelector`.

## Configurer l’installation

Avec le CLI, les trois options sont optionnelles et validées avant l’écriture
dans GitOps :

```bash
kuberpack install \
  --cluster production \
  --gitops-url https://github.com/equipe/socle.git \
  --host kuberpack.exemple.org \
  --builder-runtime-class gvisor \
  --builder-node-selector '{"kuberpack.dev/build":"true"}' \
  --builder-tolerations '[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]'
```

Le fichier de configuration non secret accepte les mêmes valeurs sous forme de
chaînes JSON :

```yaml
builderRuntimeClass: gvisor
builderNodeSelector: '{"kuberpack.dev/build":"true"}'
builderTolerations: '[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]'
```

Pour une installation Helm directe, utiliser les valeurs du chart :

```yaml
builderPlacement:
  runtimeClass: gvisor
  nodeSelector:
    kuberpack.dev/build: "true"
  tolerations:
    - key: dedicated
      operator: Equal
      value: build
      effect: NoSchedule
```

Le control plane reçoit ces valeurs par les variables d’environnement
suivantes, puis les reporte sur le Pod du Job :

- `KUBERPACK_BUILDER_RUNTIME_CLASS` ;
- `KUBERPACK_BUILDER_NODE_SELECTOR` (objet JSON) ;
- `KUBERPACK_BUILDER_TOLERATIONS` (tableau JSON).

Avec Helm, ne pas définir ces variables séparément des valeurs du chart : la
politique d’admission épingle le placement rendu par le chart, et un écart
entre les deux ferait refuser les Jobs légitimes.

## Validation

Une valeur fournie est refusée si elle est inutilisable :

- RuntimeClass vide ou nom DNS Kubernetes valide ; un chemin ou un espace est
  refusé ;
- sélecteur : objet JSON de labels Kubernetes, 16 entrées au maximum, clés et
  valeurs valides, aucune valeur vide ;
- tolérances : tableau JSON de 16 entrées au maximum, champs Kubernetes connus,
  clé obligatoire, opérateur `Equal` ou `Exists`, effet `NoSchedule`,
  `PreferNoSchedule` ou `NoExecute`, `tolerationSeconds` seulement avec
  `NoExecute`. Une tolérance sans clé, qui accepterait tous les taints, est
  refusée.

Une valeur vide conserve le défaut. Si un RuntimeClass est demandé mais absent
du cluster, le Pod reste en attente : le build échoue, sans dégrader le reste
de la plateforme.

Voir aussi [le builder](builder.md), [l’installation](install.md) et la
[section sécurité](security.md).

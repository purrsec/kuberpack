# Matrice réseau : application réelle des NetworkPolicy

## Pourquoi cette matrice

Une NetworkPolicy présente dans l'API Kubernetes n'est qu'une intention. Le
kubelet ne l'applique pas : il faut un CNI ou un agent de politique qui la
traduise en règles. Un cluster peut donc afficher `NetworkPolicy` dans
`kubectl get networkpolicy` et laisser tout le trafic passer.

Kuberpack ne suppose plus l'application. `kuberpack install` détecte le moteur
réseau et avertit quand il est inconnu ou désactivé. `kuberpack doctor network`
observe ensuite le trafic réel entre deux pods, dans un namespace temporaire,
sans lire de Secret.

## Ce que fait la détection

La détection est en lecture seule. Elle inspecte :

- la version du serveur (`+k3s`), les annotations `k3s.io/node-args` ;
- les DaemonSets de `kube-system` (`kube-router`, `calico-node`, `cilium`,
  `anetd`, `aws-node`, `azure-npm`, `azure-vnet`) ;
- les ConfigMaps de configuration (`cilium-config`, `amazon-vpc-cni`) ;
- les labels et `providerID` des nœuds pour reconnaître EKS, GKE et AKS.

Elle ne lit jamais de Secret. Les preuves et les avertissements sont affichés
pour être contestables : un opérateur doit pouvoir vérifier la conclusion.

## Matrice

| Plateforme / CNI | Détection | Application des NetworkPolicy | Limites et pièges | Statut dans cette passe |
| --- | --- | --- | --- | --- |
| **k3s / kube-router** | version `+k3s` ou DaemonSet `kube-router` | Contrôleur kube-router intégré à k3s ; actif sauf `--disable-network-policy` | Policies additives ; vérifier l'argument sur chaque nœud ; le contrôleur intégré n'apparaît pas toujours comme un pod séparé | **Testé** sur le banc Docker k3s `v1.37.1+k3s1` le 23 octobre 2026 : `doctor network` a observé joignable avant deny-all, bloqué après, puis supprimé le namespace |
| **EKS / VPC CNI, mode standard** | DaemonSet `aws-node`, ConfigMap `amazon-vpc-cni`, env `NETWORK_POLICY_ENFORCING_MODE=standard` | **Opt-in** : `enable-network-policy-controller=true`, `--enable-network-policy=true`, agent `aws-eks-nodeagent` et CRD `policyendpoints.networking.k8s.aws` | Seuls les pods **sélectionnés** par une policy sont isolés ; une policy additive ou un pod non sélectionné garde son trafic. Fargate, Windows, nœuds GPU, `hostNetwork` et Multus ne sont pas couverts. Les pods sans `ownerReferences` peuvent ne pas être appliqués de façon fiable : la probe utilise un Deployment et des Jobs. | **Non testé** sans compte EKS |
| **EKS / VPC CNI, mode strict** | même détection, `NETWORK_POLICY_ENFORCING_MODE=strict` | Refus par défaut pour les pods sans policy, en plus des règles explicites | Comportement dépendant de la version de l'agent ; vérifier que l'agent est réellement actif et que le mode strict est supporté par l'add-on | **Non testé** sans compte EKS |
| **GKE / Dataplane V2 (Cilium, `anetd`)** | DaemonSet `anetd` (parfois `cilium`), ConfigMap `cilium-config` | NetworkPolicy appliquée par Cilium ; `policy-enforcement` valant `always`/`default` applique, `never` désactive, `audit` journalise sans bloquer | Dataplane V2 dépend de la version GKE ; les policies de cluster Cilium peuvent élargir ; les FQDN policies sont hors du modèle Kuberpack | **Non testé** sans projet GKE |
| **GKE / Dataplane V1 + Calico** | DaemonSet `calico-node`, add-on NetworkPolicy du cluster | Calico applique les NetworkPolicy si l'add-on a été activé | L'add-on doit être activé sur le cluster ; les `GlobalNetworkPolicy` Calico peuvent précéder les policies de namespace | **Non testé** sans projet GKE |
| **AKS / Azure NPM** | DaemonSet `azure-npm` | Azure Network Policy Manager applique les policies si le cluster a été créé/mis à jour avec `networkPolicy=azure` | Un `azure-vnet` sans `azure-npm` n'applique rien ; vérifier `networkProfile.networkPolicy` | **Non testé** sans abonnement Azure |
| **AKS / Calico** | DaemonSet `calico-node` | Calico applique les policies si `networkPolicy=calico` | Même limites que Calico ; le moteur choisi est figé à la création du cluster | **Non testé** sans abonnement Azure |
| **CNI inconnu / aucun moteur** | aucune preuve concluante | Susceptible d'ignorer complètement les NetworkPolicy | `kuberpack install` affiche un avertissement explicite et recommande la probe ; celle-ci peut rester indéterminée | Signalé |

## Preuve exécutée sur k3s

Le banc Docker k3s `v1.37.1+k3s1` (API `https://172.29.90.11:6443`) a exécuté :

```sh
kuberpack doctor network --context default \
  --kubeconfig "$HOME/.kube/k3s-test.yaml" --yes
```

Résultat observé :

```
Moteur réseau détecté : kube-router (k3s)
Kubernetes : v1.37.1+k3s1
Application des NetworkPolicy : attendue
Contrôle positif (avant policy) : joignable
Deny-all (après policy) : bloqué
Verdict : NetworkPolicy appliquée (refus observé)
Nettoyage : namespace supprimé
```

Le cluster n'expose pas de DaemonSet `kube-router` : le contrôleur est intégré au
serveur k3s, et la détection s'appuie sur la version `+k3s`. Aucun namespace ni
objet `netcheck` ne restait après la commande. Ce résultat ne remplace pas une
validation EKS, GKE ou AKS.

## Le piège des policies additives

Les NetworkPolicy sont **additives** : pour un pod, l'ensemble des règles
autorisées est l'union de toutes les policies qui le sélectionnent. Ajouter une
policy `allow-all` dans le namespace, ou une policy de cluster qui autorise
tout, rouvre le réseau sans modifier les policies Kuberpack.

Conséquences :

- une policy `deny-all` n'est pas un état définitif : une autre équipe, un
  chart, un opérateur ou un admin de namespace peut ajouter une autorisation ;
- `kuberpack doctor network` est une **preuve ponctuelle**, pas une surveillance
  continue ; il faut le rejouer après un changement de policy ou de CNI ;
- le contrôle d'admission des NetworkPolicy, la propriété GitOps du namespace
  et, sur EKS, les `ClusterNetworkPolicy` de tier Admin sont les mécanismes qui
  empêchent réellement l'ajout d'une policy permissive. Ils ne sont pas fournis
  par cette passe.

## Modes EKS : standard et strict

Sur EKS, le VPC CNI n'active pas les NetworkPolicy par défaut. Le manifeste
amont contient `enable-network-policy-controller: "false"` dans la ConfigMap
`amazon-vpc-cni` et `--enable-network-policy=false` sur le conteneur
`aws-eks-nodeagent`. La détection le signale comme **désactivée**.

Quand l'agent est actif :

- **standard** (`NETWORK_POLICY_ENFORCING_MODE=standard`, valeur par défaut du
  manifeste) : seuls les pods sélectionnés par une NetworkPolicy sont isolés.
  Un pod sans policy, ou un pod qu'une policy additive autorise, garde son
  trafic ;
- **strict** : l'agent applique un refus par défaut aux pods sans policy, en
  plus des règles explicites. Ce mode doit être confirmé sur la version de
  l'agent installée ; il ne remplace pas les exclusions Fargate/Windows/GPU.

La probe utilise un Deployment et des Jobs, donc des pods avec
`ownerReferences`, pour éviter la limite documentée par EKS sur les pods
standalone.

## GKE : Dataplane V2 et Calico

- **Dataplane V2** s'appuie sur Cilium, déployé sous le nom `anetd` (parfois
  `cilium`). Les NetworkPolicy sont appliquées par Cilium ; `policy-enforcement`
  à `never` les désactive, `audit` les journalise seulement. Les policies
  Cilium de cluster et les FQDN policies ne font pas partie du modèle Kuberpack.
- **Dataplane V1 + Calico** exige l'add-on NetworkPolicy. Sans lui, les objets
  restent présents mais ne sont pas appliqués.
- La version de GKE et le mode du plan de données changent les détails ; la
  probe réelle reste la référence.

## Lancer le diagnostic

```sh
# Lecture seule : détection + commandes manuelles.
kuberpack doctor network --context k3s-test --detect-only

# Probe réelle : crée un namespace temporaire, deux pods et un deny-all,
# observe le trafic, puis supprime le namespace. Aucun Secret n'est lu.
kuberpack doctor network --context k3s-test --yes

# Sur un cluster de production, --yes est obligatoire après vérification de la
# cible ; le contexte courant implicite est toujours refusé.
kuberpack doctor network --context eks-production --kubeconfig ~/.kube/config --yes
```

La commande refuse un contexte vide et exige `--yes` pour toute probe. Le
message est renforcé quand le nom du contexte ressemble à une production
(`prod`, `production`, `prd`, `live`). Elle rend un code non nul si le deny-all
ne bloque pas le trafic, si la probe est indéterminée ou si le namespace n'a
pas pu être supprimé.

### Ce que la probe prouve

1. contrôle positif : un pod client joint un pod cible avant toute policy ;
2. deny-all : une NetworkPolicy `podSelector: {}`, `policyTypes:
   [Ingress, Egress]` est créée dans le namespace ;
3. contrôle négatif : un nouveau pod client ne joint plus la cible ; trois
   tentatives couvrent la propagation du CNI ;
4. nettoyage : le namespace temporaire est supprimé.

Si le contrôle positif échoue (image non tirable, CNI cassé, réseau du banc),
le verdict est **indéterminé** : un refus ensuite ne prouverait rien.

### RBAC minimal

Le diagnostic n'a besoin que de :

- `nodes` et `daemonsets` : get, list (détection) ;
- `configmaps` dans `kube-system` : get, list (mode Cilium/VPC CNI) ;
- `namespaces` : create, get, delete ;
- `deployments` : create ;
- `pods` : list, get, `pods/log` ;
- `jobs` : create, get ;
- `networkpolicies` : create.

La version du serveur (`/version`) est aussi lue. Aucun accès aux Secrets
n'est nécessaire. Le code ne contient aucun appel
`Secrets()` ; les tests unitaires vérifient qu'aucune action sur les Secrets
n'est émise.

## Limites connues

- Le test est **ponctuel** et ne détecte pas une policy permissive ajoutée
  après coup.
- Il couvre **pod vers pod en TCP** dans un namespace temporaire : pas le DNS,
  les Services, `node`/`localhost`, `hostNetwork`, le trafic externe, ni les
  politiques L7.
- Il dépend d'une image busybox tirable par le cluster (`--image`).
- EKS : Fargate, Windows, GPU, Multus et `hostNetwork` ne sont pas couverts ;
  le mode standard n'isole que les pods sélectionnés.
- GKE/AKS : la matrice dépend de la version et de l'activation du moteur ;
  ces plateformes n'ont pas été testées dans cette passe.
- Une politique de cluster (EKS `ClusterNetworkPolicy`, Calico
  `GlobalNetworkPolicy`, Cilium clusterwide) peut autoriser ce que la policy de
  namespace refuse ; le diagnostic ne l'inspecte pas.
- Les NetworkPolicy natives ne filtrent pas les noms de domaine et ne couvrent
  pas tous les flux avec le nœud ou `localhost`.

## Sources

- [Kubernetes : NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [EKS : Network Policy](https://docs.aws.amazon.com/eks/latest/userguide/cni-network-policy.html)
- [VPC CNI : Network Policy FAQ](https://github.com/aws/amazon-vpc-cni-k8s/blob/master/docs/network-policy-faq.md)
- [VPC CNI : manifeste amont](https://github.com/aws/amazon-vpc-cni-k8s/blob/master/config/master/aws-k8s-cni.yaml)
- [AWS Network Policy Agent](https://github.com/aws/aws-network-policy-agent)
- [GKE : Network Policy](https://cloud.google.com/kubernetes-engine/docs/how-to/network-policy)
- [GKE : Dataplane V2](https://cloud.google.com/kubernetes-engine/docs/concepts/dataplane-v2)

Ces documents décrivent les modes des fournisseurs. Les résultats du banc
d'essai ne concernent que les versions effectivement exécutées.

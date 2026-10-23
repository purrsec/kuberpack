// Package netcheck détecte le moteur réseau du cluster et vérifie que les
// NetworkPolicy sont réellement appliquées. Il ne lit jamais de Secret : la
// détection se limite aux DaemonSets, ConfigMaps, nœuds et à la version du
// serveur ; la probe crée un namespace temporaire explicitement désigné.
package netcheck

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Engine identifie le moteur qui applique les NetworkPolicy.
type Engine string

const (
	EngineUnknown    Engine = "inconnu"
	EngineCalico     Engine = "calico"
	EngineCilium     Engine = "cilium"
	EngineAWSVPC     Engine = "aws-vpc-cni"
	EngineAzureNPM   Engine = "azure-npm"
	EngineAzureCNI   Engine = "azure-cni"
	EngineKubeRouter Engine = "kube-router"
)

// Enforcement résume ce que la détection permet d'affirmer. Une policy
// présente dans l'API ne prouve rien : seule la probe ou une observation
// réelle du trafic donne une preuve.
type Enforcement string

const (
	EnforcementExpected Enforcement = "attendue"
	EnforcementDisabled Enforcement = "désactivée"
	EnforcementAudit    Enforcement = "audit seule"
	EnforcementUnknown  Enforcement = "inconnue"
)

// Detection est le résultat de l'inspection du cluster. Les preuves sont
// volontairement citées : un opérateur doit pouvoir contester la conclusion.
type Detection struct {
	Engine            Engine
	Platform          string
	KubernetesVersion string
	Version           string
	Mode              string
	Enforcement       Enforcement
	Evidence          []string
	Warnings          []string
}

// Enforced indique si le moteur est supposé appliquer les NetworkPolicy.
// Cette valeur reste une attente : RunProbe fournit la preuve du trafic.
func (d Detection) Enforced() bool { return d.Enforcement == EnforcementExpected }

// Label rend le moteur lisible dans le rapport.
func (e Engine) Label() string {
	switch e {
	case EngineCalico:
		return "Calico"
	case EngineCilium:
		return "Cilium"
	case EngineAWSVPC:
		return "AWS VPC CNI"
	case EngineAzureNPM:
		return "Azure Network Policy Manager"
	case EngineAzureCNI:
		return "Azure CNI sans moteur de policy"
	case EngineKubeRouter:
		return "kube-router (k3s)"
	case EngineUnknown:
		return "inconnu"
	default:
		return string(e)
	}
}

type analysis struct {
	engine      Engine
	version     string
	mode        string
	enforcement Enforcement
	evidence    []string
	warnings    []string
}

// Detect inspecte kube-system et les nœuds. Une erreur d'inspection partielle
// n'annule pas les preuves déjà réunies : elle est renvoyée avec la détection
// pour que l'appelant l'affiche sans bloquer l'installation.
func Detect(ctx context.Context, client kubernetes.Interface) (Detection, error) {
	d := Detection{Engine: EngineUnknown, Enforcement: EnforcementUnknown}
	var problems []string
	k3s := false

	if info, err := client.Discovery().ServerVersion(); err != nil {
		problems = append(problems, fmt.Sprintf("version du serveur indisponible: %v", err))
	} else if info != nil {
		d.KubernetesVersion = info.GitVersion
		if isK3sVersion(info.GitVersion) {
			k3s = true
			d.Evidence = append(d.Evidence, "version Kubernetes "+info.GitVersion+" (+k3s)")
		}
	}

	var nodes []corev1.Node
	if list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err != nil {
		problems = append(problems, fmt.Sprintf("nœuds illisibles: %v", err))
	} else {
		nodes = list.Items
		d.Platform = detectPlatform(nodes)
	}

	k3sDisabled := k3sNetworkPolicyDisabled(nodes)

	var daemonsets []appsv1.DaemonSet
	if list, err := client.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{}); err != nil {
		problems = append(problems, fmt.Sprintf("DaemonSets de kube-system illisibles: %v", err))
	} else {
		daemonsets = list.Items
	}
	byName := map[string]appsv1.DaemonSet{}
	for _, ds := range daemonsets {
		byName[ds.Name] = ds
	}

	var found []analysis
	if ds, ok := byName["calico-node"]; ok {
		found = append(found, analyzeCalico(ds))
	}
	if ds, ok := byName["cilium"]; ok {
		found = append(found, analyzeCilium(ctx, client, ds, false))
	}
	if ds, ok := byName["anetd"]; ok {
		// GKE Dataplane V2 embarque Cilium sous le nom anetd.
		found = append(found, analyzeCilium(ctx, client, ds, true))
	}
	if ds, ok := byName["aws-node"]; ok {
		found = append(found, analyzeAWS(ctx, client, ds))
	}
	if ds, ok := byName["azure-npm"]; ok {
		found = append(found, analyzeAzureNPM(ds))
	}
	if ds, ok := byName["azure-vnet"]; ok {
		if _, npm := byName["azure-npm"]; !npm {
			found = append(found, analyzeAzureCNI(ds))
		}
	}
	if ds, ok := byName["kube-router"]; ok {
		found = append(found, analyzeKubeRouter(ds, k3sDisabled))
	} else if k3s {
		found = append(found, analyzeKubeRouter(appsv1.DaemonSet{}, k3sDisabled))
	}

	chosen := choose(found)
	if chosen != nil {
		d.Engine = chosen.engine
		d.Version = chosen.version
		d.Mode = chosen.mode
		d.Enforcement = chosen.enforcement
		d.Evidence = append(d.Evidence, chosen.evidence...)
		d.Warnings = append(d.Warnings, chosen.warnings...)
	}
	if len(found) > 1 {
		var names []string
		for _, candidate := range found {
			names = append(names, candidate.engine.Label())
		}
		sort.Strings(names)
		d.Warnings = append(d.Warnings, "plusieurs moteurs réseau détectés ("+strings.Join(names, ", ")+") : vérifier lequel applique réellement les NetworkPolicy et retirer l'autre.")
	}
	if d.Engine == EngineUnknown {
		d.Warnings = append(d.Warnings, "moteur réseau non identifié : la présence d'un objet NetworkPolicy ne prouve pas son application. Exécuter `kuberpack doctor network --context <contexte> --yes` et consulter docs/network-matrix.md.")
	} else if d.Enforcement == EnforcementUnknown {
		d.Warnings = append(d.Warnings, "le moteur est connu mais son mode d'application n'a pas pu être déterminé : valider par une probe réelle.")
	}

	if len(problems) > 0 {
		return d, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return d, nil
}

func choose(found []analysis) *analysis {
	priority := []Engine{EngineCalico, EngineCilium, EngineAWSVPC, EngineAzureNPM, EngineAzureCNI, EngineKubeRouter}
	for _, engine := range priority {
		for i := range found {
			if found[i].engine == engine {
				return &found[i]
			}
		}
	}
	return nil
}

func analyzeCalico(ds appsv1.DaemonSet) analysis {
	return analysis{
		engine:      EngineCalico,
		version:     imageVersion(ds, "calico-node"),
		enforcement: EnforcementExpected,
		evidence:    []string{"DaemonSet kube-system/calico-node"},
		warnings:    []string{"Calico applique les NetworkPolicy ; vérifier FelixConfiguration et les GlobalNetworkPolicy, qui peuvent précéder ou élargir les policies de namespace."},
	}
}

func analyzeCilium(ctx context.Context, client kubernetes.Interface, ds appsv1.DaemonSet, gke bool) analysis {
	a := analysis{engine: EngineCilium, enforcement: EnforcementExpected}
	if gke {
		a.evidence = append(a.evidence, "DaemonSet kube-system/anetd (GKE Dataplane V2, Cilium)")
		a.warnings = append(a.warnings, "GKE Dataplane V2 : l'application dépend de la version du plan de données ; la probe réelle reste la référence.")
	} else {
		a.evidence = append(a.evidence, "DaemonSet kube-system/cilium")
	}
	a.version = imageVersion(ds, "cilium-agent")
	if a.version == "" {
		a.version = imageVersion(ds, "")
	}

	config, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "cilium-config", metav1.GetOptions{})
	switch {
	case err == nil:
		a.evidence = append(a.evidence, "ConfigMap kube-system/cilium-config")
		if disabled, audit := ciliumEnforcement(config.Data); disabled {
			a.enforcement = EnforcementDisabled
			a.warnings = append(a.warnings, "Cilium désactive l'application des NetworkPolicy (policy-enforcement=never, enable-policy=false ou enable-k8s-networkpolicy=false).")
		} else if audit {
			a.enforcement = EnforcementAudit
			a.warnings = append(a.warnings, "Cilium est en mode audit : les flux sont journalisés mais pas bloqués.")
		}
	case !apierrors.IsNotFound(err):
		a.warnings = append(a.warnings, fmt.Sprintf("ConfigMap cilium-config illisible: %v", err))
	}
	if gke && a.version == "" {
		a.warnings = append(a.warnings, "version de Cilium/Anetd inconnue : vérifier la version de GKE et le mode Dataplane V2.")
	}
	return a
}

func ciliumEnforcement(data map[string]string) (disabled, audit bool) {
	for _, key := range []string{"enable-k8s-networkpolicy", "enable-policy", "policy-enforcement"} {
		value := strings.ToLower(strings.TrimSpace(data[key]))
		switch value {
		case "never", "false", "disabled":
			disabled = true
		case "audit":
			audit = true
		}
	}
	return disabled, audit
}

func analyzeAWS(ctx context.Context, client kubernetes.Interface, ds appsv1.DaemonSet) analysis {
	a := analysis{engine: EngineAWSVPC, enforcement: EnforcementExpected}
	a.version = imageVersion(ds, "aws-node")
	a.evidence = append(a.evidence, "DaemonSet kube-system/aws-node")
	a.mode = strings.ToLower(strings.TrimSpace(containerEnv(ds, "aws-node", "NETWORK_POLICY_ENFORCING_MODE")))

	enabled := containerArg(ds, "aws-eks-nodeagent", "--enable-network-policy=true") || containerArg(ds, "aws-eks-nodeagent", "--enable-network-policy")
	disabled := containerArg(ds, "aws-eks-nodeagent", "--enable-network-policy=false")
	agentPresent := hasContainer(ds, "aws-eks-nodeagent")
	if agentPresent {
		a.evidence = append(a.evidence, "conteneur aws-eks-nodeagent")
	}

	configValue := ""
	config, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "amazon-vpc-cni", metav1.GetOptions{})
	switch {
	case err == nil:
		a.evidence = append(a.evidence, "ConfigMap kube-system/amazon-vpc-cni")
		configValue = strings.ToLower(strings.TrimSpace(config.Data["enable-network-policy-controller"]))
	case !apierrors.IsNotFound(err):
		a.warnings = append(a.warnings, fmt.Sprintf("ConfigMap amazon-vpc-cni illisible: %v", err))
	}
	// Un argument explicite sur l'agent est prioritaire ; sinon la ConfigMap
	// fait foi. Une ConfigMap absente ou à false laisse l'agent désactivé.
	switch {
	case enabled:
		disabled = false
	case disabled:
	case configValue == "true":
		enabled = true
	case configValue == "false" || configValue == "":
		disabled = true
	}

	switch {
	case disabled || !enabled:
		a.enforcement = EnforcementDisabled
		a.warnings = append(a.warnings, "AWS VPC CNI : l'agent NetworkPolicy est désactivé (enable-network-policy-controller=false, --enable-network-policy=false ou agent absent). Les NetworkPolicy ne sont pas appliquées.")
	case a.mode == "strict":
		a.warnings = append(a.warnings, "AWS VPC CNI en mode strict : refus par défaut pour tous les pods ; vérifier la version de l'agent et les exclusions Fargate/Windows.")
	default:
		a.warnings = append(a.warnings, "AWS VPC CNI en mode standard : seuls les pods sélectionnés par une policy sont isolés ; une policy additive ou un pod non sélectionné garde son trafic. Vérifier enable-network-policy-controller=true et PolicyEndpoint.")
	}
	if !agentPresent {
		a.warnings = append(a.warnings, "conteneur aws-eks-nodeagent absent : version de VPC CNI trop ancienne ou fonctionnalité non installée.")
	}
	return a
}

func analyzeAzureNPM(ds appsv1.DaemonSet) analysis {
	return analysis{
		engine:      EngineAzureNPM,
		version:     imageVersion(ds, "azure-npm"),
		enforcement: EnforcementExpected,
		evidence:    []string{"DaemonSet kube-system/azure-npm"},
		warnings:    []string{"Azure NPM n'applique les policies que si le cluster AKS a été créé ou mis à jour avec networkPolicy=azure ; vérifier la santé du DaemonSet."},
	}
}

func analyzeAzureCNI(ds appsv1.DaemonSet) analysis {
	return analysis{
		engine:      EngineAzureCNI,
		version:     imageVersion(ds, "azure-vnet"),
		enforcement: EnforcementUnknown,
		evidence:    []string{"DaemonSet kube-system/azure-vnet sans azure-npm"},
		warnings:    []string{"Azure CNI est présent mais aucun moteur de policy (azure-npm ou calico-node) n'a été détecté : les NetworkPolicy peuvent être ignorées."},
	}
}

func analyzeKubeRouter(ds appsv1.DaemonSet, disabled bool) analysis {
	a := analysis{engine: EngineKubeRouter, enforcement: EnforcementExpected}
	if ds.Name != "" {
		a.version = imageVersion(ds, "kube-router")
		a.evidence = append(a.evidence, "DaemonSet kube-system/kube-router")
	} else {
		a.evidence = append(a.evidence, "k3s intègre le contrôleur NetworkPolicy de kube-router")
	}
	if disabled {
		a.enforcement = EnforcementDisabled
		a.warnings = append(a.warnings, "k3s : l'argument --disable-network-policy est présent sur un nœud ; les NetworkPolicy ne sont pas appliquées.")
	} else {
		a.warnings = append(a.warnings, "k3s/kube-router : vérifier l'absence de --disable-network-policy et noter que les policies sont additives.")
	}
	return a
}

func isK3sVersion(version string) bool {
	return strings.Contains(strings.ToLower(version), "+k3s")
}

func k3sNetworkPolicyDisabled(nodes []corev1.Node) bool {
	for _, node := range nodes {
		if strings.Contains(node.Annotations["k3s.io/node-args"], "--disable-network-policy") {
			return true
		}
	}
	return false
}

func detectPlatform(nodes []corev1.Node) string {
	for _, node := range nodes {
		switch {
		case node.Labels["eks.amazonaws.com/nodegroup"] != "":
			return "EKS"
		case node.Labels["cloud.google.com/gke-nodepool"] != "":
			return "GKE"
		case node.Labels["kubernetes.azure.com/cluster"] != "":
			return "AKS"
		}
	}
	for _, node := range nodes {
		switch {
		case strings.HasPrefix(node.Spec.ProviderID, "aws://"):
			return "AWS"
		case strings.HasPrefix(node.Spec.ProviderID, "gce://"):
			return "GCP"
		case strings.HasPrefix(node.Spec.ProviderID, "azure://"):
			return "Azure"
		}
	}
	return ""
}

func imageVersion(ds appsv1.DaemonSet, container string) string {
	for _, candidate := range ds.Spec.Template.Spec.Containers {
		if container != "" && candidate.Name != container {
			continue
		}
		image := candidate.Image
		if at := strings.LastIndex(image, "@"); at >= 0 {
			return image[at+1:]
		}
		slash := strings.LastIndex(image, "/")
		if colon := strings.LastIndex(image, ":"); colon > slash {
			return image[colon+1:]
		}
	}
	return ""
}

func containerEnv(ds appsv1.DaemonSet, container, name string) string {
	for _, candidate := range ds.Spec.Template.Spec.Containers {
		if container != "" && candidate.Name != container {
			continue
		}
		for _, variable := range candidate.Env {
			if variable.Name == name {
				return variable.Value
			}
		}
	}
	return ""
}

func containerArg(ds appsv1.DaemonSet, container, argument string) bool {
	for _, candidate := range ds.Spec.Template.Spec.Containers {
		if container != "" && candidate.Name != container {
			continue
		}
		for _, value := range candidate.Args {
			if value == argument {
				return true
			}
		}
	}
	return false
}

func hasContainer(ds appsv1.DaemonSet, name string) bool {
	for _, candidate := range ds.Spec.Template.Spec.Containers {
		if candidate.Name == name {
			return true
		}
	}
	return false
}

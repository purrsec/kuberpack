package netcheck

import (
	"fmt"
	"strings"
)

// RenderDetection rend la détection lisible sans prétendre à une preuve.
func RenderDetection(d Detection) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Moteur réseau détecté : %s\n", d.Engine.Label())
	if d.Platform != "" {
		fmt.Fprintf(&out, "Plateforme : %s\n", d.Platform)
	}
	if d.KubernetesVersion != "" {
		fmt.Fprintf(&out, "Kubernetes : %s\n", d.KubernetesVersion)
	}
	if d.Version != "" {
		fmt.Fprintf(&out, "Version du moteur : %s\n", d.Version)
	}
	if d.Mode != "" {
		fmt.Fprintf(&out, "Mode d'application : %s\n", d.Mode)
	}
	fmt.Fprintf(&out, "Application des NetworkPolicy : %s\n", d.Enforcement)
	if len(d.Evidence) > 0 {
		out.WriteString("Preuves :\n")
		for _, evidence := range d.Evidence {
			fmt.Fprintf(&out, "  - %s\n", evidence)
		}
	}
	if len(d.Warnings) > 0 {
		out.WriteString("Avertissements :\n")
		for _, warning := range d.Warnings {
			fmt.Fprintf(&out, "  - %s\n", warning)
		}
	}
	return out.String()
}

// RenderProbe rend les observations de trafic. Le verdict est volontairement
// limité au scénario testé : pod vers pod dans un namespace temporaire.
func RenderProbe(report ProbeReport) string {
	var out strings.Builder
	out.WriteString("Diagnostic réseau (probe réelle) :\n")
	if report.Namespace != "" {
		fmt.Fprintf(&out, "  Namespace temporaire : %s\n", report.Namespace)
	}
	fmt.Fprintf(&out, "  Contrôle positif (avant policy) : %s\n", report.Positive)
	fmt.Fprintf(&out, "  Deny-all (après policy) : %s\n", report.Negative)
	fmt.Fprintf(&out, "  Verdict : %s\n", verdictLabel(report.Verdict))
	if report.Cleanup != "" {
		fmt.Fprintf(&out, "  Nettoyage : %s\n", report.Cleanup)
	}
	if len(report.Notes) > 0 {
		out.WriteString("Détails :\n")
		for _, note := range report.Notes {
			fmt.Fprintf(&out, "  - %s\n", note)
		}
	}
	return out.String()
}

func verdictLabel(verdict Verdict) string {
	switch verdict {
	case VerdictEnforced:
		return "NetworkPolicy appliquée (refus observé)"
	case VerdictNotEnforced:
		return "NetworkPolicy NON appliquée (trafic encore possible)"
	default:
		return "indéterminée (probe non concluante)"
	}
}

// RenderManual rend les limites et les commandes de vérification manuelle.
// Ces commandes ne lisent aucun Secret : elles inspectent le CNI et le trafic.
func RenderManual(detection Detection) string {
	var out strings.Builder
	out.WriteString("Limites de ce diagnostic :\n")
	for _, limitation := range []string{
		"preuve ponctuelle : les policies sont additives ; une policy allow-all ajoutée plus tard peut rouvrir le trafic, et ce test ne le détecte pas ;",
		"couvre uniquement pod vers pod en TCP dans un namespace temporaire, pas le DNS, les Services, node/localhost, hostNetwork ni le trafic externe ;",
		"EKS VPC CNI : Fargate, Windows, les nœuds GPU, hostNetwork et Multus ne sont pas couverts ; l'agent NetworkPolicy doit être activé et le mode standard n'isole que les pods sélectionnés ;",
		"GKE Dataplane V2/Calico, AKS et Calico ne sont pas testés par cette passe : le verdict dépend de leur version et de leur activation ;",
		"l'image de test doit être tirable par le cluster cible (--image) ; une erreur d'image rend la probe indéterminée ;",
		"aucun Secret n'est lu ni écrit ; le namespace temporaire et ses objets sont supprimés à la fin.",
	} {
		fmt.Fprintf(&out, "  - %s\n", limitation)
	}

	out.WriteString("\nCommandes manuelles (contexte explicitement désigné) :\n")
	out.WriteString("  # La présence d'un objet NetworkPolicy ne prouve pas son application.\n")
	out.WriteString("  kubectl --context <contexte> -n <namespace> get networkpolicy,deploy,pod -o wide\n")
	out.WriteString("  kubectl --context <contexte> apply -f - <<'EOF'\n")
	out.WriteString("  apiVersion: networking.k8s.io/v1\n")
	out.WriteString("  kind: NetworkPolicy\n")
	out.WriteString("  metadata:\n")
	out.WriteString("    name: deny-all\n")
	out.WriteString("    namespace: <namespace>\n")
	out.WriteString("  spec:\n")
	out.WriteString("    podSelector: {}\n")
	out.WriteString("    policyTypes: [Ingress, Egress]\n")
	out.WriteString("  EOF\n")
	out.WriteString("  kubectl --context <contexte> -n <namespace> run probe --rm -it --restart=Never \\\n")
	out.WriteString("    --image=" + DefaultImage + " -- wget -q -T 3 -O - http://<ip-cible>:" + fmt.Sprint(ProbePort) + "/\n")

	switch detection.Engine {
	case EngineKubeRouter:
		out.WriteString("\n  # k3s/kube-router : vérifier --disable-network-policy sur les nœuds.\n")
		out.WriteString("  kubectl --context <contexte> get nodes -o jsonpath='{range .items[*]}{.metadata.name}{\" \"}{.metadata.annotations.k3s\\.io/node-args}{\"\\n\"}{end}'\n")
	case EngineCalico:
		out.WriteString("\n  # Calico : vérifier Felix et les policies globales.\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get ds calico-node\n")
		out.WriteString("  kubectl --context <contexte> get felixconfiguration default -o yaml\n")
		out.WriteString("  kubectl --context <contexte> get globalnetworkpolicies.crd.projectcalico.org\n")
	case EngineCilium:
		out.WriteString("\n  # Cilium / GKE Dataplane V2 : vérifier le mode d'application.\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get cm cilium-config -o yaml | grep -E 'enable-policy|policy-enforcement|enable-k8s-networkpolicy'\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system exec ds/cilium -- cilium-dbg status --verbose\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get ds anetd 2>/dev/null\n")
	case EngineAWSVPC:
		out.WriteString("\n  # EKS VPC CNI : l'agent est opt-in ; le mode standard n'isole que les pods sélectionnés.\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get cm amazon-vpc-cni -o jsonpath='{.data.enable-network-policy-controller}{\"\\n\"}'\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get ds aws-node -o yaml | grep -E 'enable-network-policy|NETWORK_POLICY_ENFORCING_MODE'\n")
		out.WriteString("  kubectl --context <contexte> get crd policyendpoints.networking.k8s.aws\n")
	case EngineAzureNPM, EngineAzureCNI:
		out.WriteString("\n  # AKS : vérifier le moteur choisi à la création du cluster.\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get ds azure-npm calico-node\n")
		out.WriteString("  az aks show -g <groupe> -n <cluster> --query networkProfile.networkPolicy\n")
	default:
		out.WriteString("\n  # Moteur inconnu : inventorier les DaemonSets réseau de kube-system.\n")
		out.WriteString("  kubectl --context <contexte> -n kube-system get ds -o wide\n")
	}
	out.WriteString("\n  Consulter docs/network-matrix.md pour la matrice EKS/GKE/k3s et les limites du modèle natif.\n")
	return out.String()
}

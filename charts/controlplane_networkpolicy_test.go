package charts

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

// renderControlPlaneNetworkPolicy rend le template d'egress du control plane.
func renderControlPlaneNetworkPolicy(t *testing.T, values map[string]any) string {
	t.Helper()
	chart, err := loader.Load("kuberpack")
	if err != nil {
		t.Fatal(err)
	}
	if values == nil {
		values = map[string]any{}
	}
	if _, ok := values["image"]; !ok {
		values["image"] = "example.invalid/kuberpack:test"
	}
	if _, ok := values["host"]; !ok {
		values["host"] = "kuberpack.example.com"
	}
	renderValues, err := chartutil.ToRenderValues(chart, values,
		chartutil.ReleaseOptions{Name: "kuberpack", Namespace: "kuberpack", IsInstall: true},
		&chartutil.Capabilities{KubeVersion: chartutil.KubeVersion{Version: "v1.33.0"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := engine.Render(chart, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	return files["kuberpack/templates/controlplane-networkpolicy.yaml"]
}

// La NetworkPolicy d'egress du control plane est livrée désactivée : une
// liste incomplète des CIDR internes casserait la réconciliation.
func TestControlPlaneEgressIsDisabledByDefault(t *testing.T) {
	rendered := renderControlPlaneNetworkPolicy(t, nil)
	if strings.TrimSpace(rendered) != "" {
		t.Fatalf("une NetworkPolicy a été rendue par défaut:\n%s", rendered)
	}
}

func TestControlPlaneEgressRendersAllowedCIDRs(t *testing.T) {
	rendered := renderControlPlaneNetworkPolicy(t, map[string]any{
		"controlPlaneNetworkPolicy": map[string]any{
			"enabled":      true,
			"allowedCIDRs": []any{"10.43.0.0/16", "192.168.1.0/24"},
		},
	})
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal([]byte(rendered), &policy); err != nil {
		t.Fatalf("le template ne produit pas un NetworkPolicy valide: %v\n%s", err, rendered)
	}
	if policy.Name != "kuberpack-control-plane-egress" || policy.Namespace != "kuberpack" {
		t.Fatalf("identité inattendue: %s/%s", policy.Namespace, policy.Name)
	}
	if policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "kuberpack" {
		t.Fatalf("sélecteur de pods inattendu: %v", policy.Spec.PodSelector)
	}
	foundPrivate := false
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock == nil {
				continue
			}
			if peer.IPBlock.CIDR == "10.43.0.0/16" || peer.IPBlock.CIDR == "192.168.1.0/24" {
				foundPrivate = true
			}
		}
	}
	if !foundPrivate {
		t.Fatalf("les CIDR déclarés ne sont pas dans la politique:\n%s", rendered)
	}
}

// Le trafic public est autorisé sans liste, mais jamais vers les plages
// privées ou le service de métadonnées.
func TestControlPlaneEgressExcludesPrivateRanges(t *testing.T) {
	rendered := renderControlPlaneNetworkPolicy(t, map[string]any{
		"controlPlaneNetworkPolicy": map[string]any{"enabled": true},
	})
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal([]byte(rendered), &policy); err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock == nil || peer.IPBlock.CIDR != "0.0.0.0/0" {
				continue
			}
			for _, excluded := range []string{"10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "192.168.0.0/16"} {
				if !contains(peer.IPBlock.Except, excluded) {
					t.Fatalf("%s doit être exclu de l'egress public", excluded)
				}
			}
			return
		}
	}
	t.Fatal("la règle egress publique est absente")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

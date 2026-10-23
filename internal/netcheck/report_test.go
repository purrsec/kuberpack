package netcheck

import (
	"strings"
	"testing"
)

func TestConfirmProbeRefusesImplicitOrUnconfirmedContext(t *testing.T) {
	tests := []struct {
		name    string
		context string
		server  string
		yes     bool
		wantErr string
	}{
		{name: "contexte vide", yes: true, wantErr: "--context"},
		{name: "contexte non confirmé", context: "k3s-test", server: "https://10.0.0.1:6443", wantErr: "--yes"},
		{name: "production non confirmée", context: "prod-eu", server: "https://eks.prod.exemple:443", wantErr: "production"},
		{name: "production confirmée", context: "prod-eu", server: "https://eks.prod.exemple:443", yes: true},
		{name: "banc confirmé", context: "k3s-test", server: "https://10.0.0.1:6443", yes: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ConfirmProbe(test.context, test.server, test.yes)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("erreur inattendue: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("erreur %v sans %q", err, test.wantErr)
			}
		})
	}
}

func TestIsProductionContext(t *testing.T) {
	for _, name := range []string{"prod", "production", "eu-prod", "prod-eu", "staging.prd", "live"} {
		if !IsProductionContext(name) {
			t.Fatalf("%q devrait ressembler à une production", name)
		}
	}
	for _, name := range []string{"k3s-test", "staging", "product", "nonprod", "dev"} {
		if IsProductionContext(name) {
			t.Fatalf("%q ne devrait pas ressembler à une production", name)
		}
	}
}

func TestRenderDetectionStatesUnknownEngineAndLimits(t *testing.T) {
	detection, err := detectForTest(t, "v1.30.4")
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderDetection(detection)
	for _, expected := range []string{"Moteur réseau détecté : inconnu", "Application des NetworkPolicy : inconnue", "ne prouve pas son application"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rapport sans %q:\n%s", expected, rendered)
		}
	}
}

func TestRenderProbeDistinguishesEnforcedAndUnenforced(t *testing.T) {
	enforced := RenderProbe(ProbeReport{
		Namespace: "netcheck-test", Positive: OutcomeReachable, Negative: OutcomeBlocked,
		Verdict: VerdictEnforced, Cleanup: "namespace supprimé",
	})
	for _, expected := range []string{"NetworkPolicy appliquée", "Contrôle positif", "Deny-all", "netcheck-test", "namespace supprimé"} {
		if !strings.Contains(enforced, expected) {
			t.Fatalf("rapport sans %q:\n%s", expected, enforced)
		}
	}
	unenforced := RenderProbe(ProbeReport{
		Namespace: "netcheck-test", Positive: OutcomeReachable, Negative: OutcomeReachable,
		Verdict: VerdictNotEnforced, Cleanup: "namespace supprimé",
	})
	if !strings.Contains(unenforced, "NON appliquée") {
		t.Fatalf("verdict non appliqué mal rendu:\n%s", unenforced)
	}
}

func TestRenderManualExplainsEngineAndLimits(t *testing.T) {
	rendered := RenderManual(Detection{Engine: EngineAWSVPC})
	for _, expected := range []string{
		"Limites de ce diagnostic",
		"policies sont additives",
		"EKS VPC CNI",
		"kubectl --context",
		"policyTypes: [Ingress, Egress]",
		"enable-network-policy-controller",
		"docs/network-matrix.md",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("commandes manuelles sans %q:\n%s", expected, rendered)
		}
	}

	k3s := RenderManual(Detection{Engine: EngineKubeRouter})
	if !strings.Contains(k3s, "--disable-network-policy") {
		t.Fatalf("commande k3s absente:\n%s", k3s)
	}
}

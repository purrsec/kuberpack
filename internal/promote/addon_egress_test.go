package promote

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	networkingv1 "k8s.io/api/networking/v1"
	k8syaml "sigs.k8s.io/yaml"
)

// parseNetworkPolicy décode une policy générée. Une policy qui ne se décode
// pas n'est pas une policy : le test doit échouer sur un document invalide,
// pas sur une chaîne absente.
func parseNetworkPolicy(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("the policy is not valid YAML: %v\n%s", err, raw)
	}
	if doc["kind"] != "NetworkPolicy" {
		t.Fatalf("the document is not a NetworkPolicy:\n%s", raw)
	}
	// Le document doit aussi se décoder dans le type de l'API Kubernetes :
	// un YAML lisible mais mal formé serait refusé par le serveur.
	var typed networkingv1.NetworkPolicy
	if err := k8syaml.Unmarshal([]byte(raw), &typed); err != nil {
		t.Fatalf("the policy is refused by the Kubernetes type: %v\n%s", err, raw)
	}
	if typed.APIVersion != "networking.k8s.io/v1" || typed.Kind != "NetworkPolicy" {
		t.Fatalf("the policy has no API identity: %s/%s", typed.APIVersion, typed.Kind)
	}
	return doc
}

func policyTypes(t *testing.T, doc map[string]any) []string {
	t.Helper()
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("the policy has no spec: %v", doc)
	}
	raw, ok := spec["policyTypes"].([]any)
	if !ok {
		t.Fatalf("the policy does not declare its types: %v", spec)
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		out = append(out, value.(string))
	}
	return out
}

// Une policy doit garder l'ingress du projet et fermer l'egress : DNS, API
// et opérateur quand le moteur en a besoin, réplication, et le stockage
// nommé par backup.s3.
func TestTheAddonPolicyDeclaresIngressAndBoundedEgress(t *testing.T) {
	policy := addonNetworkPolicy("demo-postgres", "demo", "postgres", "postgres", "demo", []int{5432},
		addonEgress{
			KubernetesAPI:     true,
			OperatorNamespace: "cnpg-system",
			OperatorPorts:     []int{443, 9090},
			ReplicaPorts:      []int{5432},
			Peers:             []addonEgressPeer{{Kind: "s3", AddonName: "rustfs", Ports: []int{9000, 443}}},
		},
		AddonSpec{ServiceClients: []string{"web"}})
	doc := parseNetworkPolicy(t, policy)
	types := policyTypes(t, doc)
	if len(types) != 2 || types[0] != "Ingress" || types[1] != "Egress" {
		t.Fatalf("the policy types are %v, want [Ingress Egress]", types)
	}
	spec := doc["spec"].(map[string]any)
	if _, ok := spec["ingress"]; !ok {
		t.Error("the existing ingress is gone")
	}
	if _, ok := spec["egress"]; !ok {
		t.Error("the policy does not declare its egress")
	}
	for _, want := range []string{
		"kubernetes.io/metadata.name: kube-system",
		"k8s-app: kube-dns",
		"{ protocol: UDP, port: 53 }",
		"{ protocol: TCP, port: 53 }",
		"kubernetes.io/metadata.name: cnpg-system",
		"kuberpack.io/addon: s3",
		"kuberpack.io/addon-name: rustfs",
		"kuberpack.io/addon-name: postgres",
		"{ protocol: TCP, port: 9000 }",
		"{ protocol: TCP, port: 443 }",
		"{ protocol: TCP, port: 5432 }",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not carry %q:\n%s", want, policy)
		}
	}
	for _, forbidden := range []string{"0.0.0.0/0", "::/0", "ipBlock:", "cidr:"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("the policy opens the Internet with %q:\n%s", forbidden, policy)
		}
	}
}

// Un moteur sans opérateur ne doit pas nommer un namespace qui n'existe
// pas. La règle de l'API n'est pas écrite non plus pour un moteur qui ne
// parle pas à l'API.
func TestAnEngineWithoutOperatorNeverNamesItsNamespace(t *testing.T) {
	policy := addonNetworkPolicy("demo-cache", "demo", "valkey", "cache", "demo", []int{6379},
		addonEgress{ReplicaPorts: []int{6379}}, AddonSpec{})
	parseNetworkPolicy(t, policy)
	if strings.Contains(policy, "cnpg-system") {
		t.Errorf("the policy names the operator of another engine:\n%s", policy)
	}
	if strings.Contains(policy, "{ protocol: TCP, port: 443 }") {
		t.Errorf("the policy reaches the API without needing it:\n%s", policy)
	}
	for _, want := range []string{
		"policyTypes: [Ingress, Egress]",
		"kuberpack.io/addon-name: cache",
		"{ protocol: TCP, port: 6379 }",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not carry %q:\n%s", want, policy)
		}
	}
}

// Un pod sans besoin sortant n'a qu'une règle, le DNS. Il n'obtient jamais
// la règle qui le laisserait joindre tout l'Internet.
func TestAnAddonWithoutOutgoingNeedHasNoTotalEgress(t *testing.T) {
	policy := addonNetworkPolicy("demo-cache", "demo", "valkey", "cache", "demo", []int{6379},
		addonEgress{}, AddonSpec{})
	parseNetworkPolicy(t, policy)
	if got := strings.Count(policy, "    - to:"); got != 1 {
		t.Fatalf("the policy has %d egress rules, want only the DNS:\n%s", got, policy)
	}
	for _, forbidden := range []string{"0.0.0.0/0", "::/0", "ipBlock:", "cidr:"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("the policy opens the Internet with %q:\n%s", forbidden, policy)
		}
	}
}

// Un helper joint ce qu'il utilise et rien d'autre : l'API pour le
// bootstrap des credentials, la base pour un dump, le stockage pour un
// bucket.
func TestTheHelperEgressIsBoundedByWhatItUses(t *testing.T) {
	bootstrap := addonHelperNetworkPolicy("demo-mongo", "demo", "demo", "mongo", "bootstrap", true, nil)
	parseNetworkPolicy(t, bootstrap)
	for _, want := range []string{
		"policyTypes: [Egress]",
		"kuberpack.io/helper: bootstrap",
		"kuberpack.io/addon-name: mongo",
		"kuberpack.io/for: demo",
		"k8s-app: kube-dns",
		"{ protocol: TCP, port: 443 }",
	} {
		if !strings.Contains(bootstrap, want) {
			t.Errorf("the bootstrap policy does not carry %q:\n%s", want, bootstrap)
		}
	}

	dump := addonHelperNetworkPolicy("demo-postgres", "demo", "demo", "postgres", "dump", false,
		[]addonEgressPeer{{Kind: "postgres", AddonName: "postgres", Ports: []int{5432}}})
	parseNetworkPolicy(t, dump)
	for _, want := range []string{
		"kuberpack.io/helper: dump",
		"kuberpack.io/addon: postgres",
		"kuberpack.io/addon-name: postgres",
		"{ protocol: TCP, port: 5432 }",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("the dump policy does not carry %q:\n%s", want, dump)
		}
	}
	if strings.Contains(dump, "port: 6443") {
		t.Errorf("the dump policy reaches the API without needing it:\n%s", dump)
	}

	bucket := addonHelperNetworkPolicy("demo-postgres", "demo", "demo", "postgres", "bucket", false,
		[]addonEgressPeer{{Kind: "s3", AddonName: "rustfs", Ports: []int{9000, 443}}})
	parseNetworkPolicy(t, bucket)
	for _, want := range []string{
		"kuberpack.io/helper: bucket",
		"kuberpack.io/addon: s3",
		"kuberpack.io/addon-name: rustfs",
		"{ protocol: TCP, port: 9000 }",
	} {
		if !strings.Contains(bucket, want) {
			t.Errorf("the bucket policy does not carry %q:\n%s", want, bucket)
		}
	}
	if strings.Contains(bucket, "port: 6443") {
		t.Errorf("the bucket policy reaches the API without needing it:\n%s", bucket)
	}

	for name, policy := range map[string]string{"bootstrap": bootstrap, "dump": dump, "bucket": bucket} {
		for _, forbidden := range []string{"0.0.0.0/0", "::/0", "ipBlock:", "cidr:"} {
			if strings.Contains(policy, forbidden) {
				t.Errorf("the %s policy opens the Internet with %q:\n%s", name, forbidden, policy)
			}
		}
	}
}

// L'addon rendu déclare son egress et ses policies de helpers, et les Jobs
// helpers portent les labels que ces policies sélectionnent.
func TestTheRenderedAddonsCloseTheirEgressAndLabelTheirHelpers(t *testing.T) {
	dir := t.TempDir()
	engine, ok := AddonEngineFor("cnpg")
	if !ok {
		t.Fatal("the engine cnpg is not available")
	}
	if _, err := engine.Render(dir, AddonSpec{
		Name:           "postgres",
		Project:        "demo",
		Namespace:      "demo",
		Backup:         &Backup{S3: "rustfs", Retention: "30d"},
		Siblings:       map[string]string{"rustfs": "s3"},
		ServiceClients: []string{"web"},
	}, Platform{}); err != nil {
		t.Fatal(err)
	}
	policy := readFile(t, dir, "networkpolicy.yaml")
	if !strings.Contains(policy, "policyTypes: [Ingress, Egress]") {
		t.Errorf("the rendered policy does not close the egress:\n%s", policy)
	}
	for _, want := range []string{"kubernetes.io/metadata.name: cnpg-system", "kuberpack.io/addon-name: rustfs", "port: 9000"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the rendered policy does not carry %q:\n%s", want, policy)
		}
	}

	helpers := readFile(t, dir, "helper-networkpolicy.yaml")
	for _, want := range []string{"kuberpack.io/helper: dump", "kuberpack.io/helper: bucket", "kuberpack.io/addon-name: rustfs"} {
		if !strings.Contains(helpers, want) {
			t.Errorf("the rendered helper policy does not carry %q:\n%s", want, helpers)
		}
	}
	dump := readFile(t, dir, "dump.yaml")
	for _, want := range []string{"kuberpack.io/helper: dump", "kuberpack.io/addon-name: postgres"} {
		if !strings.Contains(dump, want) {
			t.Errorf("the dump Job does not carry %q:\n%s", want, dump)
		}
	}
	bucket := readFile(t, dir, "bucket-job.yaml")
	for _, want := range []string{"kuberpack.io/helper: bucket", "kuberpack.io/addon-name: postgres"} {
		if !strings.Contains(bucket, want) {
			t.Errorf("the bucket Job does not carry %q:\n%s", want, bucket)
		}
	}
	kustomization := readFile(t, dir, "kustomization.yaml")
	if !strings.Contains(kustomization, "helper-networkpolicy.yaml") {
		t.Errorf("the helper policy is not activated:\n%s", kustomization)
	}
}

// Le Job de bootstrap d'une base sans opérateur joint quand même l'API, et
// sa policy est celle d'un helper, pas celle de la base.
func TestTheBootstrapHelperOnlyReachesTheAPI(t *testing.T) {
	dir := t.TempDir()
	engine, ok := AddonEngineFor("mongo")
	if !ok {
		t.Fatal("the engine mongo is not available")
	}
	if _, err := engine.Render(dir, AddonSpec{Name: "mongo", Project: "demo", Namespace: "demo"}, Platform{}); err != nil {
		t.Fatal(err)
	}
	policy := readFile(t, dir, "helper-networkpolicy.yaml")
	parseNetworkPolicy(t, policy)
	for _, want := range []string{
		"kuberpack.io/helper: bootstrap",
		"kuberpack.io/addon-name: mongo",
		"kubernetes.io/metadata.name: kube-system",
		"{ protocol: TCP, port: 443 }",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the bootstrap policy does not carry %q:\n%s", want, policy)
		}
	}
	if strings.Contains(policy, "0.0.0.0/0") || strings.Contains(policy, "ipBlock:") {
		t.Errorf("the bootstrap policy opens the Internet:\n%s", policy)
	}
	job := readFile(t, dir, "bootstrap-job.yaml")
	for _, want := range []string{
		"kuberpack.io/helper: bootstrap",
		"kuberpack.io/addon-name: mongo",
		"kuberpack.io/for: demo",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("the bootstrap Job does not carry %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "kuberpack.io/addon: nosql") {
		t.Errorf("the bootstrap pod must not be selected as an instance of the database:\n%s", job)
	}
}

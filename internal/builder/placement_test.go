package builder

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Le Job porte le RuntimeClass et le pool demandés par l'opérateur.
func TestNewJobUsesTheConfiguredPlacement(t *testing.T) {
	runner := &JobRunner{
		Client:       fake.NewSimpleClientset(),
		Image:        "registry.test/builder:1",
		RuntimeClass: "gvisor",
		NodeSelector: map[string]string{"kuberpack.dev/build": "true"},
		Tolerations: []corev1.Toleration{{
			Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "build", Effect: corev1.TaintEffectNoSchedule,
		}},
	}
	job, err := runner.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Fatalf("RuntimeClass non transmis: %#v", pod.RuntimeClassName)
	}
	if pod.NodeSelector["kubernetes.io/arch"] != "amd64" || pod.NodeSelector["kuberpack.dev/build"] != "true" {
		t.Fatalf("sélecteur de nœuds incomplet: %#v", pod.NodeSelector)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "dedicated" {
		t.Fatalf("tolérances non transmises: %#v", pod.Tolerations)
	}
	// Le runner ne doit pas partager sa carte avec le Job : une mutation
	// ultérieure de l'un ne déplace pas le build de l'autre.
	pod.NodeSelector["kuberpack.dev/build"] = "false"
	if runner.NodeSelector["kuberpack.dev/build"] != "true" {
		t.Fatal("le Job a modifié le sélecteur du runner")
	}
}

// Sans réglage, le Job garde exactement le noyau partagé et l'architecture
// historiques : le chemin d'isolation reste optionnel.
func TestNewJobKeepsTheSharedKernelByDefault(t *testing.T) {
	runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "registry.test/builder:1"}
	job, err := runner.NewJob(testRequest())
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	if pod.RuntimeClassName != nil {
		t.Fatalf("un RuntimeClass a été imposé par défaut: %q", *pod.RuntimeClassName)
	}
	if len(pod.NodeSelector) != 1 || pod.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Fatalf("le sélecteur par défaut a changé: %#v", pod.NodeSelector)
	}
	if len(pod.Tolerations) != 0 {
		t.Fatalf("des tolérances ont été imposées par défaut: %#v", pod.Tolerations)
	}
}

// Une configuration construite directement, sans passer par l'installateur,
// est refusée avant la création du Job.
func TestNewJobRefusesInvalidPlacement(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*JobRunner)
	}{
		{"RuntimeClass invalide", func(r *JobRunner) { r.RuntimeClass = "Gvisor" }},
		{"RuntimeClass avec chemin", func(r *JobRunner) { r.RuntimeClass = "pools/gvisor" }},
		{"label de nœud invalide", func(r *JobRunner) { r.NodeSelector = map[string]string{"bad key": "true"} }},
		{"valeur de label vide", func(r *JobRunner) { r.NodeSelector = map[string]string{"kuberpack.dev/build": ""} }},
		{"tolérance sans clé", func(r *JobRunner) {
			r.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
		}},
		{"tolérance Equal sans valeur", func(r *JobRunner) {
			r.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual}}
		}},
		{"tolérance avec effet inconnu", func(r *JobRunner) {
			r.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: "Sometimes"}}
		}},
		{"toléranceSeconds sans NoExecute", func(r *JobRunner) {
			seconds := int64(60)
			r.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: &seconds}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runner := &JobRunner{Client: fake.NewSimpleClientset(), Image: "registry.test/builder:1"}
			test.configure(runner)
			if _, err := runner.NewJob(testRequest()); err == nil {
				t.Fatal("configuration de placement invalide acceptée")
			}
		})
	}
}

func TestParsePlacementNormalizesTheDefaults(t *testing.T) {
	selector, err := ParseNodeSelector(`{"kuberpack.dev/build":"true"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(selector) != 1 || selector["kuberpack.dev/build"] != "true" {
		t.Fatalf("sélecteur lu: %#v", selector)
	}
	// ParseNodeSelector ne fusionne pas l'architecture : cette fusion est
	// faite au rendu du Job, et le test ci-dessus la vérifie.
	if _, ok := selector["kubernetes.io/arch"]; ok {
		t.Fatal("ParseNodeSelector a inventé un label non fourni")
	}

	tolerations, err := ParseTolerations(`[{"key":"dedicated","value":"build","effect":"NoSchedule"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tolerations) != 1 || tolerations[0].Operator != corev1.TolerationOpEqual {
		t.Fatalf("l'opérateur par défaut doit être Equal: %#v", tolerations)
	}
	if normalized, err := NormalizeNodeSelector(selector); err != nil {
		t.Fatal(err)
	} else if normalized["kubernetes.io/arch"] != "amd64" || normalized["kuberpack.dev/build"] != "true" {
		t.Fatalf("fusion du sélecteur: %#v", normalized)
	}
}

func TestParseNodeSelectorRefusesMalformedInput(t *testing.T) {
	for name, raw := range map[string]string{
		"tableau au lieu d'objet": `["kuberpack.dev/build"]`,
		"null":                    `null`,
		"json incomplet":          `{"kuberpack.dev/build":"true"} extra`,
		"clé invalide":            `{"bad key":"true"}`,
		"valeur vide":             `{"kuberpack.dev/build":""}`,
		"valeur non chaîne":       `{"kuberpack.dev/build":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseNodeSelector(raw); err == nil {
				t.Fatalf("sélecteur refusé attendu: %s", raw)
			}
		})
	}
	if _, err := ParseNodeSelector(""); err != nil {
		t.Fatalf("une valeur vide garde le défaut: %v", err)
	}
}

func TestParseTolerationsRefusesDangerousInput(t *testing.T) {
	for name, raw := range map[string]string{
		"null":                   `null`,
		"champ inconnu":          `[{"key":"dedicated","operator":"Exists","effect":"NoSchedule","surprise":true}]`,
		"tolérance universelle":  `[{"operator":"Exists"}]`,
		"opérateur inconnu":      `[{"key":"dedicated","operator":"Sometimes"}]`,
		"effet inconnu":          `[{"key":"dedicated","operator":"Exists","effect":"Sometimes"}]`,
		"Exists avec valeur":     `[{"key":"dedicated","operator":"Exists","value":"build"}]`,
		"Equal sans valeur":      `[{"key":"dedicated","operator":"Equal"}]`,
		"tolerationSeconds seul": `[{"key":"dedicated","operator":"Exists","tolerationSeconds":30}]`,
		"tolerationSeconds nul":  `[{"key":"dedicated","operator":"Exists","effect":"NoExecute","tolerationSeconds":0}]`,
		"json incomplet":         `[] extra`,
		"objet au lieu de liste": `{"key":"dedicated"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTolerations(raw); err == nil {
				t.Fatalf("tolérances refusées attendues: %s", raw)
			}
		})
	}
	if _, err := ParseTolerations(""); err != nil {
		t.Fatalf("une valeur vide garde le défaut: %v", err)
	}
	// Une tolérance de pool valide reste acceptée.
	if _, err := ParseTolerations(`[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]`); err != nil {
		t.Fatalf("tolérance de pool refusée: %v", err)
	}
}

func TestPlacementBoundsRefuseHugeInputs(t *testing.T) {
	manyTolerations := make([]corev1.Toleration, maxBuildTolerations+1)
	for index := range manyTolerations {
		manyTolerations[index] = corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists}
	}
	if _, err := NormalizeTolerations(manyTolerations); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("une liste de tolérances démesurée doit être refusée: %v", err)
	}
	manyLabels := map[string]string{}
	for index := 0; index <= maxBuildNodeSelectorEntries; index++ {
		manyLabels["kuberpack.dev/label-"+strings.Repeat("x", index+1)] = "true"
	}
	if _, err := NormalizeNodeSelector(manyLabels); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("un sélecteur démesuré doit être refusé: %v", err)
	}
}

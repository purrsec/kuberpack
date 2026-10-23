package builder

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// defaultBuildArch est l'architecture des images produites par le
	// builder. Le sélecteur de nœuds la conserve, sauf si l'opérateur la
	// remplace explicitement.
	defaultBuildArch = "amd64"
	// Les valeurs de placement viennent d'une option d'installation ou
	// d'une variable d'environnement. Elles restent petites : une liste
	// démesurée n'isole pas mieux, elle rend seulement le Job illisible.
	maxBuildNodeSelectorEntries = 16
	maxBuildTolerations         = 16
	maxBuildPlacementJSON       = 8 << 10
)

// ValidateRuntimeClass vérifie le nom d'un RuntimeClass Kubernetes.
//
// Un nom vide conserve le runtime par défaut du cluster : les builds
// partagent alors le noyau des autres pods. Un nom non vide doit être un
// nom DNS valide, comme l'exige l'API Kubernetes.
func ValidateRuntimeClass(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return fmt.Errorf("invalid RuntimeClass %q: %s", name, strings.Join(errs, ", "))
	}
	return nil
}

// NormalizeNodeSelector fusionne le sélecteur de l'opérateur avec
// l'architecture par défaut du builder.
//
// La carte retournée est toujours une copie : un appelant ne peut pas
// modifier le Job d'un autre build par effet de bord. Un sélecteur vide
// garde exactement le comportement historique (nœuds amd64 uniquement).
func NormalizeNodeSelector(configured map[string]string) (map[string]string, error) {
	validated, err := validateNodeSelector(configured)
	if err != nil {
		return nil, err
	}
	selector := map[string]string{"kubernetes.io/arch": defaultBuildArch}
	for key, value := range validated {
		selector[key] = value
	}
	return selector, nil
}

// validateNodeSelector valide et copie les labels fournis, sans ajouter
// le défaut : le rendu du Job décide de l'architecture.
func validateNodeSelector(configured map[string]string) (map[string]string, error) {
	if len(configured) > maxBuildNodeSelectorEntries {
		return nil, fmt.Errorf("builder node selector holds %d labels, at most %d are accepted", len(configured), maxBuildNodeSelectorEntries)
	}
	selector := make(map[string]string, len(configured))
	for key, value := range configured {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return nil, fmt.Errorf("invalid node selector label %q: %s", key, strings.Join(errs, ", "))
		}
		if value == "" {
			return nil, fmt.Errorf("node selector label %q has an empty value", key)
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return nil, fmt.Errorf("invalid value %q for node selector label %q: %s", value, key, strings.Join(errs, ", "))
		}
		selector[key] = value
	}
	return selector, nil
}

// ParseNodeSelector lit le sélecteur JSON de l'opérateur.
//
// Une valeur vide signifie « aucun sélecteur supplémentaire » et laisse
// le défaut historique. Les champs inconnus, le JSON incomplet et les
// labels invalides sont refusés : une faute de frappe ne doit pas placer
// les builds sur des nœuds non prévus.
func ParseNodeSelector(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxBuildPlacementJSON {
		return nil, fmt.Errorf("builder node selector is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var configured map[string]string
	if err := decoder.Decode(&configured); err != nil {
		return nil, fmt.Errorf("builder node selector must be a JSON object of labels: %w", err)
	}
	if configured == nil {
		return nil, fmt.Errorf("builder node selector must be a JSON object, not null")
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("builder node selector: %w", err)
	}
	return validateNodeSelector(configured)
}

// NormalizeTolerations valide et copie les tolérances du pool de builds.
//
// Une tolérance sans clé et avec l'opérateur Exists accepte tous les
// taints : elle permettrait aux builds de quitter un pool dédié. Elle est
// refusée. Les autres valeurs restent bornées aux opérateurs et effets
// connus de Kubernetes.
func NormalizeTolerations(configured []corev1.Toleration) ([]corev1.Toleration, error) {
	if len(configured) > maxBuildTolerations {
		return nil, fmt.Errorf("builder tolerations hold %d entries, at most %d are accepted", len(configured), maxBuildTolerations)
	}
	if len(configured) == 0 {
		return nil, nil
	}
	tolerations := make([]corev1.Toleration, 0, len(configured))
	for index, toleration := range configured {
		toleration.Key = strings.TrimSpace(toleration.Key)
		toleration.Value = strings.TrimSpace(toleration.Value)
		toleration.Effect = corev1.TaintEffect(strings.TrimSpace(string(toleration.Effect)))
		if toleration.Operator == "" {
			toleration.Operator = corev1.TolerationOpEqual
		}
		if toleration.Key == "" {
			return nil, fmt.Errorf("builder toleration %d has no key: it would tolerate every taint", index)
		}
		if errs := validation.IsQualifiedName(toleration.Key); len(errs) > 0 {
			return nil, fmt.Errorf("invalid builder toleration key %q: %s", toleration.Key, strings.Join(errs, ", "))
		}
		if toleration.Value != "" {
			if errs := validation.IsValidLabelValue(toleration.Value); len(errs) > 0 {
				return nil, fmt.Errorf("invalid builder toleration value %q: %s", toleration.Value, strings.Join(errs, ", "))
			}
		}
		switch toleration.Operator {
		case corev1.TolerationOpEqual:
			if toleration.Value == "" {
				return nil, fmt.Errorf("builder toleration %d uses Equal without a value", index)
			}
		case corev1.TolerationOpExists:
			if toleration.Value != "" {
				return nil, fmt.Errorf("builder toleration %d uses Exists with a value", index)
			}
		default:
			return nil, fmt.Errorf("builder toleration %d has an unknown operator %q", index, toleration.Operator)
		}
		switch toleration.Effect {
		case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return nil, fmt.Errorf("builder toleration %d has an unknown effect %q", index, toleration.Effect)
		}
		if toleration.TolerationSeconds != nil {
			if toleration.Effect != corev1.TaintEffectNoExecute {
				return nil, fmt.Errorf("builder toleration %d sets tolerationSeconds without the NoExecute effect", index)
			}
			if *toleration.TolerationSeconds <= 0 {
				return nil, fmt.Errorf("builder toleration %d has a non-positive tolerationSeconds", index)
			}
		}
		tolerations = append(tolerations, toleration)
	}
	return tolerations, nil
}

// ParseTolerations lit les tolérances JSON de l'opérateur.
func ParseTolerations(raw string) ([]corev1.Toleration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxBuildPlacementJSON {
		return nil, fmt.Errorf("builder tolerations are too large")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var configured []corev1.Toleration
	if err := decoder.Decode(&configured); err != nil {
		return nil, fmt.Errorf("builder tolerations must be a JSON array: %w", err)
	}
	if configured == nil {
		return nil, fmt.Errorf("builder tolerations must be a JSON array, not null")
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("builder tolerations: %w", err)
	}
	return NormalizeTolerations(configured)
}

// ensureJSONEnd refuse un document JSON suivi d'un autre contenu.
func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected content after the JSON value")
	}
	return nil
}

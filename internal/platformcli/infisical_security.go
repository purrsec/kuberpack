package platformcli

import (
	"bytes"
	"fmt"

	"helm.sh/helm/v3/pkg/chart"
)

const infisicalRuntimeDeployment = "infisical-infisical-standalone-infisical"

// The pinned upstream chart uses the same ServiceAccount for its web process
// and bootstrap hook. Only the hook needs a Kubernetes credential. Do not patch
// Jobs: an interrupted bootstrap still needs to resume on reinstall.
func hardenInfisicalRuntimeChart(loaded *chart.Chart) error {
	for _, template := range loaded.Templates {
		if template.Name != "templates/infisical.yaml" {
			continue
		}
		marker := []byte("    spec:\n      serviceAccountName:")
		if !bytes.Contains(template.Data, marker) {
			return fmt.Errorf("infisical chart changed: cannot disable the runtime Kubernetes credential safely")
		}
		template.Data = bytes.Replace(template.Data, marker,
			[]byte("    spec:\n      automountServiceAccountToken: false\n      serviceAccountName:"), 1)
		return nil
	}
	return fmt.Errorf("infisical chart is missing its runtime deployment template")
}

// Flux fetches the original chart again. Apply the same deployment-only patch
// after rendering so GitOps preserves the initial install's security setting.
func infisicalRuntimePostRenderers() []any {
	return []any{map[string]any{"kustomize": map[string]any{"patches": []any{map[string]any{
		"target": map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "name": infisicalRuntimeDeployment},
		"patch":  "- op: add\n  path: /spec/template/spec/automountServiceAccountToken\n  value: false\n",
	}}}}}
}

// The database/cache may accept the Infisical web process in their own
// namespace, never an arbitrary pod from another project. Replication peers
// remain allowed by the dependency charts' built-in same-release rules.
func infisicalDatabaseNetworkPolicy() map[string]any {
	return map[string]any{
		"enabled": true, "allowExternal": false, "allowExternalEgress": false,
		"ingressNSMatchLabels":    map[string]any{"kubernetes.io/metadata.name": infisicalNamespace},
		"ingressNSPodMatchLabels": map[string]any{"component": "infisical", "app": "infisical-standalone", "release": "infisical"},
	}
}

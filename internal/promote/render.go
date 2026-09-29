package promote

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// RenderChart runs the same validation as `helm template` without a kubeconfig.
func RenderChart(chartPath, releaseName, valuesPath string) (string, error) {
	if strings.TrimSpace(chartPath) == "" {
		return "", fmt.Errorf("chart path is empty")
	}
	if strings.TrimSpace(releaseName) == "" {
		return "", fmt.Errorf("release name is empty")
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return "", fmt.Errorf("load chart: %w", err)
	}

	userVals, err := chartutil.ReadValuesFile(valuesPath)
	if err != nil {
		return "", fmt.Errorf("read values: %w", err)
	}
	vals, err := chartutil.CoalesceValues(ch, userVals)
	if err != nil {
		return "", fmt.Errorf("merge values: %w", err)
	}

	renderVals, err := chartutil.ToRenderValues(ch, vals, chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: "default",
		Revision:  1,
		IsInstall: true,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		return "", fmt.Errorf("render values: %w", err)
	}

	files, err := engine.Render(ch, renderVals)
	if err != nil {
		return "", fmt.Errorf("helm template: %w", err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	for _, name := range names {
		content := files[name]
		if strings.TrimSpace(content) == "" {
			continue
		}
		fmt.Fprintf(&buf, "---\n# Source: %s\n%s\n", name, content)
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("helm template produced no manifests")
	}
	return buf.String(), nil
}

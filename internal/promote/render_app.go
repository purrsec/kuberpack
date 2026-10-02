package promote

// Exported renderers so the project compiler can reuse the stateless chart
// contract instead of duplicating it.

// RenderValues renders the values.yaml for a stateless service.
func RenderValues(spec AppSpec, p Platform) string { return valuesYAML(spec, p) }

// RenderHelmRelease renders the HelmRelease for a stateless service.
func RenderHelmRelease(spec AppSpec, p Platform) string { return helmReleaseYAML(spec, p) }

// RenderAppKustomization renders the per-app kustomization.
func RenderAppKustomization(spec AppSpec, p Platform) string {
	return appKustomizationYAML(spec, p)
}

// RenderParentKustomization renders a parent kustomization listing resources.
func RenderParentKustomization(namespace string, resources []string) string {
	var b []byte
	b = append(b, "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"...)
	if namespace != "" {
		b = append(b, ("\nnamespace: " + namespace + "\n")...)
	}
	b = append(b, "\nresources:\n"...)
	for _, r := range resources {
		b = append(b, ("  - " + r + "\n")...)
	}
	return string(b)
}

// RenderSecretCR renders an InfisicalStaticSecret for a service. Kept here so
// the compiler stays free of Infisical internals.
func RenderSecretCR(spec AppSpec, p Platform) string { return infisicalCRContent(spec, p) }

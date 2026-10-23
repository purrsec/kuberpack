package promote

import "strings"

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

// RenderRegistryRBAC renders the right the platform has to write the
// secret that pulls the images of the registry, in the namespace of the
// application.
//
// The right lives in the GitOps repository: it is not a secret, and the
// repository is the truth of the cluster. The credential it allows to
// write stays in the cluster. The platform never reads a secret of the
// cluster, and it never writes one outside of the namespace of its own
// applications.
func RenderRegistryRBAC(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	name := strings.TrimSpace(p.ImagePullSecret)
	if name == "" || spec.Namespace == "" || spec.Image != "" {
		return ""
	}
	role := spec.Name + "-kuberpack"
	return "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: " + role +
		"\n  namespace: " + spec.Namespace +
		"\nrules:\n  - apiGroups: [\"\"]\n    resources: [secrets]\n    verbs: [create]\n" +
		"  - apiGroups: [\"\"]\n    resources: [secrets]\n    resourceNames: [" + name +
		"]\n    verbs: [update, patch]\n" +
		// The values of the application travel in a ConfigMap: the
		// platform writes it, so that a change of the manifest is
		// applied at once and not in the interval of the controller.
		"  - apiGroups: [\"\"]\n    resources: [configmaps]\n    verbs: [create, update, patch]\n" +
		// The release of the application is asked to reconcile: the
		// platform does not change it, it only asks for a reconcile.
		"  - apiGroups: [\"helm.toolkit.fluxcd.io\"]\n    resources: [helmreleases]\n    resourceNames: [" + spec.Name +
		"]\n    verbs: [get, patch]\n" +
		"---\napiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: " + role +
		"\n  namespace: " + spec.Namespace +
		"\nsubjects:\n  - kind: ServiceAccount\n    name: kuberpack\n    namespace: " + p.ControlPlaneNamespace +
		"\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: " + role + "\n"
}

// RenderStatusRBAC permits observation of this service and its pods in its
// own namespace. Published images need observation too, without registry writes.
func RenderStatusRBAC(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	namespace := releaseNamespace(spec, p)
	role := spec.Name + "-kuberpack-status"
	return "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: " + role +
		"\n  namespace: " + namespace + "\nrules:\n" +
		"  - apiGroups: [apps]\n    resources: [deployments]\n    resourceNames: [" + spec.Name + "]\n    verbs: [get]\n" +
		"  - apiGroups: [\"helm.toolkit.fluxcd.io\"]\n    resources: [helmreleases]\n    resourceNames: [" + spec.Name + "]\n    verbs: [get]\n" +
		"  - apiGroups: [apps]\n    resources: [replicasets]\n    verbs: [list]\n" +
		"  - apiGroups: [\"\"]\n    resources: [pods]\n    verbs: [list]\n" +
		"  - apiGroups: [\"\"]\n    resources: [pods/log]\n    verbs: [get]\n" +
		"  - apiGroups: [\"metrics.k8s.io\"]\n    resources: [pods]\n    verbs: [list]\n" +
		"---\napiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: " + role +
		"\n  namespace: " + namespace + "\nsubjects:\n  - kind: ServiceAccount\n    name: kuberpack\n    namespace: " + p.ControlPlaneNamespace +
		"\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: " + role + "\n"
}

// RenderCertificate renders the certificate of an application. The platform
// issues the certificates of the applications with cert-manager: the
// application is reached in HTTPS without asking Traefik for a resolver it
// may not know.
func RenderCertificate(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	if spec.TLSSecretName == "" || spec.Hostname == "" {
		return ""
	}
	return "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: " + spec.TLSSecretName +
		"\nspec:\n  secretName: " + spec.TLSSecretName +
		"\n  dnsNames:\n    - " + spec.Hostname +
		"\n  issuerRef:\n    name: " + p.Issuer +
		"\n    kind: ClusterIssuer\n"
}

// RenderSecretCR renders an InfisicalStaticSecret for a service. Kept here so
// the compiler stays free of Infisical internals.
func RenderSecretCR(spec AppSpec, p Platform) string {
	p = p.withDefaults()
	content := infisicalCRContent(spec, p)
	// The auth reference remains in the operator namespace. The project
	// owns its sync resource, so Flux applies and prunes them together.
	return strings.Replace(content, "  namespace: "+p.InfisicalNamespace+"\n", "  namespace: "+releaseNamespace(spec, p)+"\n", 1)
}

// RenderVolumeClaim renders the claim of a volume of a service. The disk
// outlives the pod: it is what a database that is a file lives on.
func RenderVolumeClaim(volume Volume, spec AppSpec, p Platform) string {
	p = p.withDefaults()
	storageClass := ""
	if p.VolumeStorageClass != "" {
		storageClass = "  storageClassName: " + p.VolumeStorageClass + "\n"
	}
	return "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: " + volumeClaimName(spec, volume) +
		"\n  namespace: " + releaseNamespace(spec, p) +
		"\nspec:\n" + storageClass + "  accessModes: [\"ReadWriteOnce\"]\n  resources:\n    requests:\n      storage: " + volume.Size + "\n"
}

// volumeClaimName is the name of the claim of a volume. It carries the name
// of the application: two applications of the same namespace do not share
// their disks.
func volumeClaimName(spec AppSpec, volume Volume) string {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return volume.Name
	}
	return name + "-" + volume.Name
}

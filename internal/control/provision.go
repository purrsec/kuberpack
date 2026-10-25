package control

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/promote"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	sigsYaml "sigs.k8s.io/yaml"
)

const tenantClusterRole = "kuberpack-tenant"

func tenantServiceAccount(project string) string { return "kuberpack-tenant-" + project }
func tenantKustomization(project string) string  { return "kuberpack-project-" + project }

func boolPointer(value bool) *bool { return &value }

// ensureProjectTenant creates the namespace, the tenant identity and the Flux
// Kustomization of a project. The namespace is created by the control plane
// itself: the tenant identity cannot create a cluster-scoped Namespace.
//
// The Kustomization applies the project with the tenant ServiceAccount. A
// control plane compromised after this point can write application manifests,
// but the Flux apply cannot exceed the tenant namespace.
func (s *Server) ensureProjectTenant(ctx context.Context, project, namespace string) error {
	if !s.cfg.TenantIsolation {
		return nil
	}
	if s.cfg.Kube == nil {
		return nil
	}
	dynamicClient, err := s.dynamicClient()
	if err != nil {
		return err
	}
	if strings.TrimSpace(namespace) == "" {
		namespace = project
	}
	controlNamespace := s.cfg.Platform.ControlPlaneNamespace
	if controlNamespace == "" {
		controlNamespace = "kuberpack"
	}
	labels := map[string]string{
		"app.kubernetes.io/part-of":          "kuberpack",
		"kuberpack.io/project":               project,
		"pod-security.kubernetes.io/enforce": "baseline",
		"pod-security.kubernetes.io/audit":   "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		"kuberpack.io/tenant-provisioned":    "true",
	}
	if err := s.ensureNamespace(ctx, namespace, labels); err != nil {
		return err
	}

	serviceAccount := tenantServiceAccount(project)
	if err := s.ensureTenantServiceAccount(ctx, controlNamespace, serviceAccount); err != nil {
		return err
	}
	if err := s.ensureTenantRoleBinding(ctx, namespace, serviceAccount, controlNamespace); err != nil {
		return err
	}
	return s.ensureTenantKustomization(ctx, dynamicClient, project, namespace, serviceAccount, controlNamespace)
}

func (s *Server) ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	client := s.cfg.Kube.CoreV1().Namespaces()
	current, err := client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("read namespace %q: %w", name, err)
	}
	changed := false
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range labels {
		if current.Labels[key] != value {
			current.Labels[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err = client.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (s *Server) ensureTenantServiceAccount(ctx context.Context, namespace, name string) error {
	client := s.cfg.Kube.CoreV1().ServiceAccounts(namespace)
	current, err := client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/part-of": "kuberpack"}},
			AutomountServiceAccountToken: boolPointer(false),
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("read tenant identity %s/%s: %w", namespace, name, err)
	}
	if current.AutomountServiceAccountToken != nil && !*current.AutomountServiceAccountToken && current.Labels["app.kubernetes.io/part-of"] == "kuberpack" {
		return nil
	}
	current.AutomountServiceAccountToken = boolPointer(false)
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	current.Labels["app.kubernetes.io/part-of"] = "kuberpack"
	_, err = client.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (s *Server) ensureTenantRoleBinding(ctx context.Context, namespace, serviceAccount, controlNamespace string) error {
	if err := s.ensureRoleBinding(ctx, namespace, tenantClusterRole, tenantClusterRole, serviceAccount, controlNamespace); err != nil {
		return err
	}
	if err := s.ensureRoleBinding(ctx, namespace, "kuberpack-inspector", "kuberpack-project-inspector", "kuberpack", controlNamespace); err != nil {
		return err
	}
	if s.cfg.Reloader {
		// The reloader has its own identity. The control plane only grants
		// it the Secrets and Deployments of this namespace; it never reads
		// a Secret itself.
		if err := s.ensureRoleBinding(ctx, namespace, "kuberpack-reloader", "kuberpack-reloader-tenant", "kuberpack-reloader", controlNamespace); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ensureRoleBinding(ctx context.Context, namespace, name, role, serviceAccount, controlNamespace string) error {
	client := s.cfg.Kube.RbacV1().RoleBindings(namespace)
	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: serviceAccount, Namespace: controlNamespace,
		}},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
	}
	current, err := client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("read tenant rolebinding %s/%s: %w", namespace, name, err)
	}
	current.Subjects = desired.Subjects
	current.RoleRef = desired.RoleRef
	_, err = client.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (s *Server) ensureTenantKustomization(ctx context.Context, dynamicClient dynamic.Interface, project, namespace, serviceAccount, controlNamespace string) error {
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	projectsDir := strings.TrimSpace(s.cfg.Platform.ProjectsDir)
	if projectsDir == "" {
		projectsDir = "projects"
	}
	appsSource := strings.TrimSpace(s.cfg.AppsSource)
	if appsSource == "" {
		appsSource = "kuberpack-gitops"
	}
	name := tenantKustomization(project)
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"name": name, "namespace": controlNamespace,
			"labels": map[string]any{"kuberpack.io/project": project, "kuberpack.io/target-namespace": namespace},
		},
		"spec": map[string]any{
			"interval":           "30s",
			"path":               "./" + filepath.ToSlash(filepath.Join(projectsDir, project)),
			"prune":              true,
			"targetNamespace":    namespace,
			"serviceAccountName": serviceAccount,
			"timeout":            "10m",
			"sourceRef": map[string]any{
				"kind": "GitRepository", "name": appsSource, "namespace": controlNamespace,
			},
			"dependsOn": []any{map[string]any{"name": "kuberpack-platform"}},
		},
	}}
	client := dynamicClient.Resource(gvr).Namespace(controlNamespace)
	current, err := client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("read project Kustomization %s: %w", name, err)
	}
	desired.SetResourceVersion(current.GetResourceVersion())
	_, err = client.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

// deleteProjectTenant removes the Flux Kustomization, the tenant identity and
// the namespace that the provisioner created. A namespace that does not carry
// the project label is left alone: it is not ours.
func (s *Server) deleteProjectTenant(ctx context.Context, project, namespace string) error {
	if !s.cfg.TenantIsolation || s.cfg.Kube == nil {
		return nil
	}
	dynamicClient, err := s.dynamicClient()
	if err != nil {
		return err
	}
	controlNamespace := s.cfg.Platform.ControlPlaneNamespace
	if controlNamespace == "" {
		controlNamespace = "kuberpack"
	}
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	if err := dynamicClient.Resource(gvr).Namespace(controlNamespace).Delete(ctx, tenantKustomization(project), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := s.cfg.Kube.CoreV1().ServiceAccounts(controlNamespace).Delete(ctx, tenantServiceAccount(project), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	for _, binding := range []string{tenantClusterRole, "kuberpack-reloader", "kuberpack-inspector"} {
		if err := s.cfg.Kube.RbacV1().RoleBindings(namespace).Delete(ctx, binding, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	current, err := s.cfg.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Labels["kuberpack.io/project"] != project {
		return nil
	}
	// Namespace deletion is a separate operator task; it may contain foreign resources.
	return nil
}

func propagationPolicy() *metav1.DeletionPropagation {
	policy := metav1.DeletePropagationBackground
	return &policy
}

// syncProjectTenantScope keeps the Infisical operator access list in the
// cluster. With tenant isolation there is no global projects Kustomization to
// apply the generated ConfigMap: the control plane updates it directly, from
// the namespaces of the applications it knows. The values contain no secret.
func (s *Server) syncProjectTenantScope(ctx context.Context) error {
	if !s.cfg.TenantIsolation || !s.cfg.Platform.InfisicalOperatorScope {
		return nil
	}
	if s.cfg.Kube == nil || s.cfg.Store == nil {
		return nil
	}
	apps, err := s.cfg.Store.ListApps(ctx)
	if err != nil {
		return err
	}
	imageApps, err := s.cfg.Store.ListImageApps(ctx)
	if err != nil {
		return err
	}
	apps = append(apps, imageApps...)
	seen := map[string]bool{s.cfg.Platform.InfisicalNamespace: true}
	for _, app := range apps {
		namespace := strings.TrimSpace(app.Namespace)
		if namespace == "" {
			continue
		}
		if strings.HasPrefix(namespace, "kube-") || namespace == "default" || namespace == "flux-system" ||
			namespace == s.cfg.Platform.ControlPlaneNamespace || namespace == s.cfg.Platform.BuildNamespace ||
			namespace == s.cfg.Platform.InfisicalNamespace {
			continue
		}
		seen[namespace] = true
	}
	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	data, err := promote.RenderInfisicalOperatorScope(s.cfg.Platform, namespaces)
	if err != nil {
		return err
	}
	var desired corev1.ConfigMap
	if err := sigsYaml.Unmarshal(data, &desired); err != nil {
		return err
	}
	controlNamespace := s.cfg.Platform.ControlPlaneNamespace
	if controlNamespace == "" {
		controlNamespace = "kuberpack"
	}
	client := s.cfg.Kube.CoreV1().ConfigMaps(controlNamespace)
	current, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, &desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	changed := false
	if current.Data["values.yaml"] != desired.Data["values.yaml"] {
		current.Data = desired.Data
		changed = true
	}
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range desired.Labels {
		if current.Labels[key] != value {
			current.Labels[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err = client.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

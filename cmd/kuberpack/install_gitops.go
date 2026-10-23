package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	embedded "git.host.bzh/pepe/kuberpack/charts"
	"git.host.bzh/pepe/kuberpack/internal/fetch"
	gitprovider "git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart/loader"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (i *installation) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = i.work
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0="+strings.TrimPrefix(fetch.AuthArgs(gitprovider.AuthScheme(i.c.GitProvider), i.credentials.Git)[1], "http.extraHeader="), "GIT_CONFIG_KEY_1=user.name", "GIT_CONFIG_VALUE_1=Kuberpack", "GIT_CONFIG_KEY_2=user.email", "GIT_CONFIG_VALUE_2=kuberpack@localhost")
	if i.c.CAFile != "" {
		cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+i.c.CAFile)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.ReplaceAll(strings.TrimSpace(string(out)), i.credentials.Git, "[redacted]"))
	}
	return strings.TrimSpace(string(out)), nil
}

func (i *installation) prepareRepository(ctx context.Context) error {
	work := i.work
	i.work = filepath.Dir(work)
	_, err := i.git(ctx, "clone", "--branch", i.c.GitOpsBranch, "--single-branch", i.c.GitOpsURL, work)
	i.work = work
	if err != nil {
		return err
	}
	root := filepath.Join(i.work, filepath.FromSlash(i.c.Root))
	marker := filepath.Join(root, "installation.yaml")
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("GitOps directory %s is not owned by Kuberpack; choose a dedicated --gitops-root", i.c.Root)
		}
	} else if err != nil {
		return err
	} else {
		var previous struct {
			ClusterUID string         `yaml:"clusterUID"`
			Config     map[string]any `yaml:"config"`
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &previous); err != nil {
			return err
		}
		ns, err := i.kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if previous.ClusterUID != "" && previous.ClusterUID != string(ns.UID) {
			return fmt.Errorf("GitOps scope %s belongs to another cluster; choose another installation name or restore the cluster deliberately", i.c.Root)
		}
	}
	return nil
}

func (i *installation) write(name string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return i.writeBytes(name, data)
}
func (i *installation) writeBytes(name string, data []byte) error {
	clean := filepath.ToSlash(filepath.Clean(name))
	if !strings.HasPrefix(clean, strings.TrimSuffix(i.c.Root, "/")+"/") {
		return fmt.Errorf("refusing to write outside installation scope: %s", name)
	}
	current := i.work
	for _, part := range strings.Split(clean, "/") {
		current = filepath.Join(current, part)
		entry, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow GitOps symlink: %s", name)
		}
	}
	full := filepath.Join(i.work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0644)
}

func (i *installation) copyCharts() error {
	return fs.WalkDir(embedded.Files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := embedded.Files.ReadFile(name)
		if err != nil {
			return err
		}
		return i.writeBytes(i.c.Root+"/charts/"+name, data)
	})
}

func (i *installation) publishPlatform(ctx context.Context) error {
	if err := i.copyCharts(); err != nil {
		return err
	}
	projects := i.c.ProjectsPath() + "/kustomization.yaml"
	if _, err := os.Stat(filepath.Join(i.work, projects)); os.IsNotExist(err) {
		if err := i.write(projects, kustomization(nil)); err != nil {
			return err
		}
	}
	if err := promote.SyncInfisicalOperatorScope(i.work, i.infisicalScopePlatform()); err != nil {
		return err
	}
	values, err := i.platformValues(ctx)
	if err != nil {
		return err
	}
	i.controlValues = values
	loaded, err := loader.Load(filepath.Join(i.work, i.c.ChartPath("kuberpack")))
	if err != nil {
		return err
	}
	// The first install supplies the CRDs/RBAC and bootstraps the control plane.
	// Every subsequent upgrade is driven by the GitOps HelmRelease below.
	resources := []string{"kuberpack.yaml"}
	if data, err := i.durableInfrastructure(ctx); err != nil {
		return err
	} else if len(data) > 0 {
		if err := i.writeBytes(i.c.PlatformPath()+"/bootstrap.yaml", data); err != nil {
			return err
		}
		resources = append(resources, "bootstrap.yaml")
	}
	control := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", i.c.Namespace, "kuberpack", map[string]any{
		"interval": "1m", "releaseName": "kuberpack", "timeout": i.c.Timeout,
		"chart":  map[string]any{"spec": map[string]any{"chart": "./" + i.c.ChartPath("kuberpack"), "reconcileStrategy": "ChartVersion", "sourceRef": map[string]any{"kind": "GitRepository", "name": i.c.Source, "namespace": i.c.Namespace}}},
		"values": values,
	})
	if err := i.write(i.c.PlatformPath()+"/kuberpack.yaml", control.Object); err != nil {
		return err
	}
	for _, service := range i.managedCharts {
		if service.Name == "kuberpack" {
			continue
		}
		sourceName := "kuberpack-" + service.Name
		repo := object("source.toolkit.fluxcd.io/v1", "HelmRepository", i.c.Namespace, sourceName, map[string]any{"interval": "1h", "url": service.Repo})
		releaseSpec := map[string]any{"interval": "5m", "releaseName": service.Name, "targetNamespace": service.Namespace, "storageNamespace": service.Namespace, "timeout": i.c.Timeout,
			"chart": map[string]any{"spec": map[string]any{"chart": service.Chart, "version": service.Version, "sourceRef": map[string]any{"kind": "HelmRepository", "name": sourceName, "namespace": i.c.Namespace}}}, "values": service.Values,
		}
		if service.Name == "infisical" {
			releaseSpec["valuesFrom"] = []any{map[string]any{"kind": "Secret", "name": "infisical-helm-values", "valuesKey": "values.yaml"}}
			releaseSpec["postRenderers"] = infisicalRuntimePostRenderers()
		}
		if service.Name == "infisical-operator" {
			// Explicit values override valuesFrom in Flux. Keep the scope in
			// the generated ConfigMap so adding a project updates both RBAC
			// and the namespaces watched by the operator.
			delete(service.Values, "scopedRBAC")
			delete(service.Values, "scopedNamespaces")
			releaseSpec["valuesFrom"] = []any{map[string]any{"kind": "ConfigMap", "name": promote.InfisicalOperatorScopeName, "valuesKey": "values.yaml"}}
		}
		rel := object("helm.toolkit.fluxcd.io/v2", "HelmRelease", i.c.Namespace, sourceName, releaseSpec)
		filename := service.Name + ".yaml"
		a, _ := yaml.Marshal(repo.Object)
		b, _ := yaml.Marshal(rel.Object)
		if err := i.writeBytes(i.c.PlatformPath()+"/"+filename, append(append(a, []byte("---\n")...), b...)); err != nil {
			return err
		}
		resources = append(resources, filename)
	}
	if len(i.managedFlux) > 0 {
		if err := i.writeBytes(i.c.PlatformPath()+"/flux.yaml", i.managedFlux); err != nil {
			return err
		}
		resources = append(resources, "flux.yaml")
	}
	if err := i.write(i.c.PlatformPath()+"/kustomization.yaml", kustomization(resources)); err != nil {
		return err
	}
	ns, err := i.kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := i.write(i.c.Root+"/installation.yaml", map[string]any{"clusterUID": string(ns.UID), "config": i.c}); err != nil {
		return err
	}
	status, err := i.git(ctx, "status", "--porcelain", "--", i.c.Root)
	if err != nil {
		return err
	}
	if status != "" {
		if _, err := i.git(ctx, "add", "--", i.c.Root); err != nil {
			return err
		}
		if _, err := i.git(ctx, "-c", "commit.gpgsign=false", "commit", "-m", "kuberpack: reconcile installation "+i.c.Cluster); err != nil {
			return err
		}
		if _, err := i.git(ctx, "push", "origin", "HEAD:"+i.c.GitOpsBranch); err != nil {
			return err
		}
	}
	if err := i.ensureHelm(ctx, clusterService{Name: "kuberpack", Chart: "kuberpack", Namespace: i.c.Namespace, Values: values}, loaded, nil); err != nil {
		return err
	}
	if err := ensureFluxSource(ctx, i.config, i.c.Namespace, i.c.Source, i.c.GitOpsURL, i.c.GitOpsBranch); err != nil {
		return err
	}
	for _, scope := range []struct {
		name, path string
		prune      bool
	}{{"kuberpack-platform", i.c.PlatformPath(), false}, {"kuberpack-projects", i.c.ProjectsPath(), true}} {
		// Refuse another reconciler of this path instead of racing two prune inventories.
		gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
		all, err := i.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, old := range all.Items {
			oldPath, _, _ := unstructured.NestedString(old.Object, "spec", "path")
			if strings.TrimPrefix(oldPath, "./") == scope.path && (old.GetName() != scope.name || old.GetNamespace() != i.c.Namespace) {
				return fmt.Errorf("GitOps path %s is already reconciled by %s/%s", scope.path, old.GetNamespace(), old.GetName())
			}
		}
		// A platform reconciliation briefly changes its readiness. Giving
		// both dependent scopes the same timer can make projects encounter
		// that window on every retry. Source events still reconcile the
		// platform immediately; its periodic drift check can be slower.
		interval := "5m"
		if scope.prune {
			interval = "30s"
		}
		obj := object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", i.c.Namespace, scope.name, map[string]any{"interval": interval, "path": "./" + scope.path, "prune": scope.prune, "sourceRef": map[string]any{"kind": "GitRepository", "name": i.c.Source}, "timeout": i.c.Timeout})
		if scope.prune {
			obj.Object["spec"].(map[string]any)["dependsOn"] = []any{map[string]any{"name": "kuberpack-platform"}}
		}
		if !scope.prune {
			health := []any{map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "name": "kuberpack", "namespace": i.c.Namespace}}
			for _, service := range i.managedCharts {
				if service.Name != "kuberpack" {
					health = append(health, map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "name": "kuberpack-" + service.Name, "namespace": i.c.Namespace})
				}
			}
			obj.Object["spec"].(map[string]any)["healthChecks"] = health
		}
		if err := i.apply(ctx, obj); err != nil {
			return err
		}
	}
	return i.record(ctx, "gitops.handed-over", "true")
}

func kustomization(resources []string) map[string]any {
	if resources == nil {
		resources = []string{}
	}
	return map[string]any{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": resources}
}

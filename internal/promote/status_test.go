package promote

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestPublishedServicesGetOnlyNamespaceScopedReadAccessForStatus(t *testing.T) {
	spec := AppSpec{Name: "web", Project: "demo", Namespace: "custom-runtime", Image: "registry/app@sha256:abc"}
	p := Platform{ControlPlaneNamespace: "custom-control"}
	text := RenderStatusRBAC(spec, p)
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(text), 4096)
	var role rbacv1.Role
	var binding rbacv1.RoleBinding
	if err := decoder.Decode(&role); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&binding); err != nil {
		t.Fatal(err)
	}
	if role.Kind != "Role" || role.Namespace != "custom-runtime" || binding.Namespace != "custom-runtime" || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != "custom-control" {
		t.Fatalf("invalid observation scope: %s", text)
	}
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" {
				t.Fatalf("status can mutate workloads: %+v", rule)
			}
		}
		for _, resource := range rule.Resources {
			switch resource {
			case "deployments", "helmreleases":
				if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "web" {
					t.Fatal("named reads are not scoped to the service")
				}
			case "replicasets", "pods":
			case "pods/log":
				if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" {
					t.Fatal("runtime logs must only permit get")
				}
			default:
				t.Fatalf("status has unrelated permissions: %+v", rule)
			}
		}
	}
}

func TestObservationRolesAreAppliedBeforeTheFirstImageExists(t *testing.T) {
	bare := setupAppsGitOps(t)
	request := ProjectRequest{GitOpsURL: bare, GitOpsBranch: "main", Manifest: ProjectManifest{Project: "demo", Namespace: "custom-runtime", Services: map[string]Service{"web": {Repository: "owner/web", Hostname: "web.example.org", Port: 8080}}}}
	result, err := EnsureProject(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Apps) != 1 || result.Apps[0].Namespace != "custom-runtime" {
		t.Fatalf("runtime location lost: %+v", result.Apps)
	}
	tree := readRemoteTree(t, bare)
	parent := readFile(t, tree, "kubernetes/vps/projects/demo/kustomization.yaml")
	if listedInKustomization([]byte(parent), "web") || !listedInKustomization([]byte(parent), "web/status-rbac.yaml") {
		t.Fatalf("observation must not wait for image activation: %s", parent)
	}
	role := readFile(t, tree, "kubernetes/vps/projects/demo/web/status-rbac.yaml")
	if !strings.Contains(role, "namespace: custom-runtime") {
		t.Fatalf("wrong runtime scope: %s", role)
	}
	again, err := EnsureProject(context.Background(), request)
	if err != nil || again.Changed {
		t.Fatalf("observation introduced non-idempotent compilation: changed=%v err=%v", again.Changed, err)
	}
}

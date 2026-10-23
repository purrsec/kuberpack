package promote

import (
	"context"
	"strings"
	"testing"
)

func TestProjectInfisicalScopeOverridesThePlatformScope(t *testing.T) {
	bare := setupAppsGitOps(t)
	m := ProjectManifest{
		Project: "demo",
		Infisical: &InfisicalScope{
			ProjectID: "11111111-2222-3333-4444-555555555555",
			AuthRef:   "demo-secrets",
			Path:      "/projects/demo",
		},
		Services: map[string]Service{
			"web": {
				Type:       "stateless",
				Repository: "demo/app",
				Port:       8080,
				Hostname:   "demo.example.test",
				Secrets:    []string{"PASSWORD"},
			},
		},
	}
	if _, err := EnsureProject(context.Background(), ProjectRequest{
		GitOpsURL: bare, GitOpsBranch: "main", Manifest: m,
		Platform: Platform{
			NetworkPolicy:      true,
			InfisicalProjectID: "platform-project",
			InfisicalAuthRef:   "platform-auth",
			InfisicalPath:      "/",
			InfisicalNamespace: "infisical",
		},
	}); err != nil {
		t.Fatal(err)
	}
	clone := readRemoteTree(t, bare)
	secret := readFile(t, clone, "kubernetes/vps/projects/demo/web/secret.yaml")
	for _, want := range []string{
		"name: demo-secrets",
		"projectId: 11111111-2222-3333-4444-555555555555",
		"secretPath: /projects/demo",
	} {
		if !strings.Contains(secret, want) {
			t.Fatalf("secret does not contain %q:\n%s", want, secret)
		}
	}
}

func TestProjectInfisicalScopeIsValidated(t *testing.T) {
	for name, scope := range map[string]*InfisicalScope{
		"missing auth":  {ProjectID: "project"},
		"missing id":    {AuthRef: "auth"},
		"relative path": {ProjectID: "project", AuthRef: "auth", Path: "projects/demo"},
		"traversal":     {ProjectID: "project", AuthRef: "auth", Path: "/projects/../other"},
		"invalid ref":   {ProjectID: "project", AuthRef: "Not A DNS Label"},
	} {
		t.Run(name, func(t *testing.T) {
			m := ProjectManifest{Project: "demo", Infisical: scope, Services: map[string]Service{}}
			if err := validateManifestFields(m); err == nil {
				t.Fatalf("expected a validation error for %+v", scope)
			}
		})
	}
}

func TestStrictSecretScopeRequiresTheProjectIdentity(t *testing.T) {
	m := ProjectManifest{
		Project: "demo",
		Services: map[string]Service{
			"web": {Repository: "demo/app", Secrets: []string{"PASSWORD"}},
		},
	}
	if err := validateProjectSecretScope(m, Platform{RequireProjectSecrets: true}); err == nil {
		t.Fatal("a project without its own Infisical project must be refused")
	}
	m.Infisical = &InfisicalScope{ProjectID: "project-id"}
	if err := validateProjectSecretScope(m, Platform{RequireProjectSecrets: true}); err != nil {
		t.Fatalf("a project with its own project id must be accepted: %v", err)
	}
	m.Infisical.AuthRef = "another-project"
	if err := validateProjectSecretScope(m, Platform{RequireProjectSecrets: true}); err == nil {
		t.Fatal("a project must not point at another project identity")
	}
}

func TestStrictSecretScopeDerivesIdentityAndPath(t *testing.T) {
	projectID, authRef, secretPath := infisicalScope(AppSpec{Project: "demo", InfisicalProjectID: "project-id"}, Platform{RequireProjectSecrets: true})
	if projectID != "project-id" || authRef != "kuberpack-demo" || secretPath != "/projects/demo" {
		t.Fatalf("scope = %q %q %q", projectID, authRef, secretPath)
	}
}

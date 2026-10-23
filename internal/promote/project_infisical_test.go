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

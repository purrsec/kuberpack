package promote

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// La rotation ne concerne que le service d'un projet qui déclare des clés :
// c'est le seul cas où un InfisicalStaticSecret synchronise le Secret que le
// pod consomme. Une preview, une application autonome ou un service sans
// secret ne portent pas l'annotation, donc ne sont jamais redémarrés.
func TestSecretRotationAnnotationIsScopedToConsumingProjectServices(t *testing.T) {
	infisical := Platform{InfisicalProjectID: "project-id"}
	pinned := "example.org/app@" + testDigest
	cases := []struct {
		name string
		spec AppSpec
		p    Platform
		want bool
	}{
		{
			name: "service de projet avec secrets",
			spec: AppSpec{Name: "demo-app", Project: "demo", Namespace: "demo", Hostname: "app.example.org", Image: pinned, Secrets: []string{"PASSWORD"}},
			p:    infisical,
			want: true,
		},
		{
			name: "service de projet sans secret",
			spec: AppSpec{Name: "demo-public", Project: "demo", Namespace: "demo", Hostname: "public.example.org", Image: pinned},
			p:    infisical,
			want: false,
		},
		{
			name: "preview",
			spec: AppSpec{Name: "demo-app-pr-1", Project: "demo", Namespace: "demo", Hostname: "pr-1.example.org", Image: pinned, Preview: true, Secrets: []string{"PASSWORD"}},
			p:    infisical,
			want: false,
		},
		{
			name: "application autonome",
			spec: AppSpec{Name: "legacy", Hostname: "legacy.example.org", Image: pinned, Secrets: []string{"PASSWORD"}},
			p:    infisical,
			want: false,
		},
		{
			name: "plateforme sans Infisical",
			spec: AppSpec{Name: "demo-app", Project: "demo", Namespace: "demo", Hostname: "app.example.org", Image: pinned, Secrets: []string{"PASSWORD"}},
			p:    Platform{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := normalizeSpec(tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			values := valuesYAML(spec, tc.p)
			got := strings.Contains(values, InfisicalAutoReloadAnnotation+": \"true\"")
			if got != tc.want {
				t.Fatalf("annotation de rotation = %v, attendu %v :\n%s", got, tc.want, values)
			}
			if strings.Contains(values, "podAnnotations") || strings.Contains(values, "templateAnnotations") {
				t.Fatalf("l'annotation de rotation doit rester au niveau du Deployment :\n%s", values)
			}
		})
	}
}

// Le compilateur écrit l'annotation dans les valeurs de Git, jamais une valeur
// de secret. Le recompiler est idempotent, et retirer les secrets retire
// l'annotation. Seuls les noms de clés sortent du manifeste : c'est la seule
// donnée de secret que le control plane (et sa base SQLite) reçoit.
func TestSecretRotationSurvivesRecompileAndKeepsValuesOutOfGit(t *testing.T) {
	bare := setupGitOps(t, "")
	m := ProjectManifest{Project: "demo", Services: map[string]Service{
		"app": {Image: "example.org/app@" + testDigest, Hostname: "app.demo.example.org", Secrets: []string{"PASSWORD", "API_KEY"}},
	}}
	req := ProjectRequest{GitOpsURL: bare, Manifest: m, Platform: Platform{InfisicalProjectID: "project-id"}}

	result, err := EnsureProject(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("le premier passage doit committer")
	}
	if len(result.Apps) != 1 {
		t.Fatalf("applications compilées : %+v", result.Apps)
	}
	// Seuls les noms de clés sont projetés vers le control plane et SQLite.
	if !reflect.DeepEqual(result.Apps[0].Secrets, []string{"PASSWORD", "API_KEY"}) {
		t.Fatalf("le control plane ne doit recevoir que des noms de clés : %+v", result.Apps[0].Secrets)
	}

	clone := readRemoteTree(t, bare)
	values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml")
	if !strings.Contains(values, "deploymentAnnotations:\n  "+InfisicalAutoReloadAnnotation+": \"true\"") {
		t.Fatalf("annotation de rotation absente des valeurs :\n%s", values)
	}
	// Le pod consomme le Secret par référence : aucune valeur n'est écrite.
	for _, key := range []string{"PASSWORD", "API_KEY"} {
		if !strings.Contains(values, "key: "+key) {
			t.Fatalf("la clé %s doit être référencée :\n%s", key, values)
		}
		if strings.Contains(values, key+": ") {
			t.Fatalf("la valeur de %s ne doit pas être dans Git :\n%s", key, values)
		}
	}

	// L'objet Infisical ne porte que des expressions : les valeurs restent
	// dans Infisical, et le Secret Kubernetes est leur seule destination.
	sync := readFile(t, clone, "kubernetes/vps/projects/demo/app/secret.yaml")
	for _, key := range []string{"PASSWORD", "API_KEY"} {
		if !strings.Contains(sync, `{{ (index . "`+key+`").Value }}`) {
			t.Fatalf("l'objet de synchronisation ne référence pas %s :\n%s", key, sync)
		}
	}

	// Recompiler sans changement ne produit ni commit ni diff.
	again, err := EnsureProject(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("une recompilation sans changement doit être idempotente")
	}
	if reread := readFile(t, readRemoteTree(t, bare), "kubernetes/vps/projects/demo/app/values.yaml"); reread != values {
		t.Fatalf("les valeurs ont changé sans changement de manifeste :\n%s", reread)
	}

	// Retirer les secrets retire l'annotation : plus de Secret synchronisé,
	// plus de redémarrage.
	service := m.Services["app"]
	service.Secrets = nil
	m.Services["app"] = service
	if _, err := EnsureProject(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	clone = readRemoteTree(t, bare)
	if values := readFile(t, clone, "kubernetes/vps/projects/demo/app/values.yaml"); strings.Contains(values, InfisicalAutoReloadAnnotation) {
		t.Fatalf("un service sans secret ne doit pas être redémarré :\n%s", values)
	}
}

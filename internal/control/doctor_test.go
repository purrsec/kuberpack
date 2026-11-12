package control

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/promote"
	"k8s.io/client-go/kubernetes/fake"
)

// The preflight must answer the questions that otherwise cost a push and a
// log dig: the chart reference, the addon operators, the certificate mode,
// the bookkeeping mode, the secrets boundary.
func TestDoctorReportsThePlatformCapabilities(t *testing.T) {
	s, tokens := v2Fixture(t)
	s.cfg.Platform = promote.Platform{
		ChartRef:            "./provisioning/charts/stateless",
		ChartSourceKind:     "GitRepository",
		SourceName:          "kuberpack-chart",
		AddonOperators:      []string{"cnpg", "valkey"},
		DisableCertManager:  true,
		TraefikCertResolver: "letsencrypt",
		APIServerBlocks:     []string{"10.43.0.1/32"},
	}
	s.cfg.SkipClusterBookkeeping = true
	s.cfg.Kube = fake.NewSimpleClientset()

	r := tokenTestRequest(t, s.Handler(), http.MethodGet, "/api/v2/doctor", tokens["admin"], "")
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var report api.Doctor
	if err := json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	byName := map[string]api.DoctorCheck{}
	for _, check := range report.Checks {
		byName[check.Name] = check
	}
	for _, name := range []string{"gitops", "chart", "addon-operators", "certificates", "bookkeeping", "secrets", "namespace-read", "api-server-egress"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("the report does not carry the %q check: %v", name, report.Checks)
		}
	}
	if !byName["chart"].OK || !strings.Contains(byName["chart"].Detail, "provisioning/charts/stateless") {
		t.Errorf("chart check: %+v", byName["chart"])
	}
	if !strings.Contains(byName["addon-operators"].Detail, "cnpg") {
		t.Errorf("addon operators check: %+v", byName["addon-operators"])
	}
	if !strings.Contains(byName["certificates"].Detail, "Traefik") {
		t.Errorf("certificates check must name the ACME mode: %+v", byName["certificates"])
	}
	if !strings.Contains(byName["bookkeeping"].Detail, "skipped") {
		t.Errorf("bookkeeping check must say it is skipped: %+v", byName["bookkeeping"])
	}
}

// An undeclared addon operator refuses every project that declares a
// database: the preflight must say so before the push.
func TestDoctorFailsWithoutAddonOperators(t *testing.T) {
	s, tokens := v2Fixture(t)
	s.cfg.Platform = promote.Platform{ChartRef: "./provisioning/charts/stateless"}
	s.cfg.Kube = fake.NewSimpleClientset()

	r := tokenTestRequest(t, s.Handler(), http.MethodGet, "/api/v2/doctor", tokens["admin"], "")
	var report api.Doctor
	if err := json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Name == "addon-operators" && check.OK {
			t.Fatalf("an empty operator list must not be reported as ready: %+v", check)
		}
	}
}

// A dry run returns the verdict of the real compiler and commits nothing:
// the GitOps repository is left exactly as it was.
func TestCompileDryRunReturnsTheVerdictWithoutCommitting(t *testing.T) {
	s, tokens := v2Fixture(t)
	s.cfg.Platform = promote.Platform{ChartRef: "./provisioning/charts/stateless", AddonOperators: []string{"cnpg"}}
	s.cfg.Kube = fake.NewSimpleClientset()

	before := gitHead(t, s.cfg.GitOpsURL)
	manifest := "project: demo\nnamespace: demo\nservices:\n  web:\n    image: docker.io/library/nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    port: 80\n    hostname: web.example.org\n"
	body := jsonBody(t, api.CompileRequest{Manifest: manifest})
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/compile", tokens["maintainer"], body)
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var result api.CompileResult
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Fatalf("the manifest must compile: %+v", result)
	}
	if result.Namespace != "demo" {
		t.Errorf("namespace: %q", result.Namespace)
	}
	if len(result.Services) != 1 || result.Services[0] != "web" {
		t.Errorf("services: %v", result.Services)
	}
	if after := gitHead(t, s.cfg.GitOpsURL); after != before {
		t.Fatalf("a dry run must not commit: %s -> %s", before, after)
	}
}

// A refusal is a result with its reason, not an opaque failure.
func TestCompileDryRunReportsTheRefusalReason(t *testing.T) {
	s, tokens := v2Fixture(t)
	s.cfg.Platform = promote.Platform{
		ChartRef:       "./provisioning/charts/stateless",
		AddonOperators: []string{"cnpg"},
		NodeMemory:     "512Mi",
		NodeReserve:    "0",
	}
	s.cfg.Kube = fake.NewSimpleClientset()

	manifest := "project: demo\nnamespace: demo\nservices:\n  web:\n    image: docker.io/library/nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    port: 80\n    hostname: web.example.org\n    resources:\n      limits: {cpu: \"1\", memory: 4Gi}\n"
	body := jsonBody(t, api.CompileRequest{Manifest: manifest})
	r := tokenTestRequest(t, s.Handler(), http.MethodPost, "/api/v2/projects/demo/compile", tokens["maintainer"], body)
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var result api.CompileResult
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Fatal("a project that does not fit the node must be refused")
	}
	if !strings.Contains(result.Refused, "memory") {
		t.Fatalf("the refusal must explain itself: %q", result.Refused)
	}
}

// gitHead reads the current commit of the GitOps repository, so a test can
// prove that nothing was written.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

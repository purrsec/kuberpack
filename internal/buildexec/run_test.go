package buildexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeTrivyCondensesCritical(t *testing.T) {
	got := summarizeTrivy([]byte(`{"Results":[{"Vulnerabilities":[
		{"VulnerabilityID":"CVE-2025-68121","PkgName":"stdlib","InstalledVersion":"v1.22.12","FixedVersion":"1.24.13, 1.25.7","Severity":"CRITICAL"},
		{"VulnerabilityID":"CVE-2025-68121","PkgName":"stdlib","InstalledVersion":"v1.22.12","FixedVersion":"1.24.13","Severity":"CRITICAL"},
		{"VulnerabilityID":"CVE-9999-1","PkgName":"stdlib","InstalledVersion":"v1.22.12","FixedVersion":"1.24.13","Severity":"CRITICAL"}
	]}]}`))
	want := "CVE-2025-68121 in stdlib (v1.22.12 → 1.24.13) +1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if summarizeTrivy([]byte(`{"Results":[]}`)) != "" {
		t.Fatal("empty report")
	}
}

func TestValidateScanReportRequiresImageTarget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{"scanned image", `{"Results":[{"Target":"image.tar (alpine 3.21)"}]}`, false},
		{"empty scan", `{"Results":[]}`, true},
		{"missing target", `{"Results":[{}]}`, true},
		{"invalid report", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trivy.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateScanReport(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateScanReport() error = %v, wantError %t", err, tc.wantError)
			}
		})
	}
}

// The compiled binary of the application is found by its name: the plan puts
// the directory of the application in the PATH of the image.
func TestAddAppPath(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.json")
	raw := `{"deploy":{"startCommand":"miniflux","variables":{"RAILPACK_VERSION":"0.39.0"}}}`
	if err := os.WriteFile(plan, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addAppPath(plan); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Deploy struct {
			StartCommand string            `json:"startCommand"`
			Variables    map[string]string `json:"variables"`
		} `json:"deploy"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Deploy.Variables["PATH"] != "/app:$PATH" {
		t.Fatalf("the PATH of the application is missing: %+v", parsed.Deploy.Variables)
	}
	if !strings.HasPrefix(parsed.Deploy.StartCommand, "export PATH=/app:$PATH; ") {
		t.Fatalf("the start command must find the binary of the application: %q", parsed.Deploy.StartCommand)
	}
}

package buildexec

import (
	"os"
	"path/filepath"
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

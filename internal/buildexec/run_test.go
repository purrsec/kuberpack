package buildexec

import (
	"os"
	"path/filepath"
	"testing"
)

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

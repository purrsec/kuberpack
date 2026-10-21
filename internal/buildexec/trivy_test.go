package buildexec

import (
	"strings"
	"testing"
)

const report = `{"Results":[{"Vulnerabilities":[
 {"VulnerabilityID":"CVE-2026-59873","PkgName":"tar","InstalledVersion":"6.2.1","FixedVersion":"7.5.19","Severity":"CRITICAL"},
 {"VulnerabilityID":"CVE-2026-00001","PkgName":"openssl","InstalledVersion":"3.0.1","FixedVersion":"3.0.2","Severity":"CRITICAL"}]}]}`

// A vulnerability the platform accepts does not stop the build; the others
// do, and they are all named.
func TestTrivyVerdictSeparatesTheAcceptedOnes(t *testing.T) {
	verdict, err := TrivyVerdictOf([]byte(report), []string{"cve-2026-59873"})
	if err != nil {
		t.Fatal(err)
	}
	if len(verdict.Accepted) != 1 {
		t.Fatalf("accepted %d, want 1", len(verdict.Accepted))
	}
	if len(verdict.Blocked) != 1 {
		t.Fatalf("blocked %d, want 1", len(verdict.Blocked))
	}
	if got := verdict.SummarizeBlocks(); !strings.Contains(got, "CVE-2026-00001") {
		t.Errorf("the summary is %q, want the blocking finding", got)
	}
	if got := strings.Join(verdict.AcceptedIDs(), ","); got != "CVE-2026-59873" {
		t.Errorf("the accepted are %q", got)
	}
}

// Without an accepted list, every finding blocks the build.
func TestTrivyVerdictBlocksEverythingByDefault(t *testing.T) {
	verdict, err := TrivyVerdictOf([]byte(report), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(verdict.Blocked) != 2 {
		t.Fatalf("blocked %d, want 2", len(verdict.Blocked))
	}
	summary := verdict.SummarizeBlocks()
	if !strings.Contains(summary, "CVE-2026-59873") || !strings.Contains(summary, "CVE-2026-00001") {
		t.Errorf("the summary is %q, want both findings", summary)
	}
}

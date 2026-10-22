package control

import (
	"strings"
	"testing"
)

// TestClipLogsKeepsTheEnd checks that the extract of a build keeps the
// end of the output, where the failure is, and that it stays bounded.
func TestClipLogsKeepsTheEnd(t *testing.T) {
	small := "the build went well\n"
	if got := clipLogs(small); got != "the build went well" {
		t.Errorf("the output was changed: %q", got)
	}

	// A long output keeps its end, and says what was dropped.
	big := strings.Repeat("noise\n", 40000) + "the failure is here\n"
	got := clipLogs(big)
	if len(got) > maxLogBytes+200 {
		t.Errorf("the extract is %d bytes, it should stay bounded", len(got))
	}
	if !strings.HasSuffix(got, "the failure is here") {
		t.Errorf("the end of the output was lost:\n%s", got[len(got)-200:])
	}
	if !strings.Contains(got, "bytes of output were dropped") {
		t.Error("the extract does not say what it dropped")
	}
}

// TestClipLogsKeepsTheLinesWhole checks that the extract does not start
// in the middle of a line.
func TestClipLogsKeepsTheLinesWhole(t *testing.T) {
	big := strings.Repeat("a line of the build\n", 20000)
	got := clipLogs(big)
	if !strings.HasPrefix(got, "...") {
		t.Error("the extract does not start with the notice")
	}
	after := got[strings.Index(got, "\n")+1:]
	if !strings.HasPrefix(after, "a line of the build") {
		t.Errorf("the extract starts in the middle of a line: %q", after[:40])
	}
}

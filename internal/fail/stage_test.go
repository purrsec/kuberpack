package fail

import (
	"errors"
	"strings"
	"testing"
)

func TestStagePrefixesAndClips(t *testing.T) {
	err := Stage(Trivy, errors.New("CVE-2025-68121 in stdlib"))
	if err == nil || err.Error() != "trivy: CVE-2025-68121 in stdlib" {
		t.Fatalf("got %v", err)
	}
	wrapped := Stage(Job, err)
	if wrapped.Error() != err.Error() {
		t.Fatalf("kept inner stage: %v", wrapped)
	}
	again := Stage(Trivy, err)
	if again.Error() != err.Error() {
		t.Fatalf("double prefix: %v", again)
	}
	long := Stage(Clone, errors.New(strings.Repeat("x", 200)))
	if !strings.HasPrefix(long.Error(), "clone: ") {
		t.Fatalf("the prefix is missing: %v", long)
	}
	if n := len([]rune(Short(long.Error()))); n != Max {
		t.Fatalf("the short message is %d runes, want %d", n, Max)
	}
}

func TestDetailKeepsGitFatal(t *testing.T) {
	got := Detail(errors.New("git clone https://git.host.bzh/pepe/x.git /tmp/x: fatal: repository not found"))
	if got != "fatal: repository not found" {
		t.Fatalf("%q", got)
	}
}

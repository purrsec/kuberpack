package oci

import "testing"

func TestTagForCommit(t *testing.T) {
	if got := TagForCommit("ABCDEF"); got != "sha-abcdef" {
		t.Fatalf("got %q", got)
	}
}

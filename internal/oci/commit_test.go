package oci

import "testing"

func TestCobayeTagForCommitPicksHighestRun(t *testing.T) {
	sha := "d45ace010726ffffffffffffffffffffffffffff"
	tag, ok := cobayeTagForCommit([]string{
		"buildcache",
		"main-9-d45ace010726",
		"main-1413-d45ace010726",
		"main-1400-deadbeefdead",
	}, sha)
	if !ok {
		t.Fatal("expected a cobaye tag")
	}
	if tag != "main-1413-d45ace010726" {
		t.Fatalf("got %q", tag)
	}
}

func TestTagForCommit(t *testing.T) {
	if got := TagForCommit("ABCDEF"); got != "sha-abcdef" {
		t.Fatalf("got %q", got)
	}
}

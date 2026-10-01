package sshkey

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	pair, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pair.PrivatePEM, "PRIVATE KEY") {
		t.Fatalf("private: %s", pair.PrivatePEM[:40])
	}
	if !strings.HasPrefix(strings.TrimSpace(pair.Public), "ssh-ed25519 ") {
		t.Fatalf("public: %q", pair.Public)
	}
}

package buildexec

import "testing"

func TestCredentialSeparationIsEnforcedByDefault(t *testing.T) {
	t.Setenv("KUBERPACK_STRICT_CREDENTIALS", "")
	if !strictTokens() {
		t.Fatal("builder allowed shared credentials by default")
	}
	if err := checkTokens("shared", "registry", tokenDigest("shared"), strictTokens()); err == nil {
		t.Fatal("builder accepted control-plane credential")
	}
}

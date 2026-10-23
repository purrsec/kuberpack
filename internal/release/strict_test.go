package release

import "testing"

func TestCredentialSeparationIsEnforcedByDefault(t *testing.T) {
	t.Setenv("KUBERPACK_STRICT_CREDENTIALS", "")
	if !strictTokens() {
		t.Fatal("control plane allowed shared builder credentials by default")
	}
}

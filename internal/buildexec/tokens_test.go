package buildexec

import "testing"

// TestCheckTokensRefusesTheControlPlaneToken checks that a builder that
// holds the token of the control plane is refused when the platform is
// strict.
func TestCheckTokensRefusesTheControlPlaneToken(t *testing.T) {
	const control = "ghp_control_plane_token"
	digest := tokenDigest(control)

	// The builder holds the token of the control plane: the build is
	// refused.
	err := checkTokens(control, "another-token", digest, true)
	if err == nil {
		t.Fatal("a builder with the token of the control plane was accepted")
	}

	// The builder holds its own tokens: nothing to say.
	if err := checkTokens("git-token", "registry-token", digest, true); err != nil {
		t.Fatalf("a builder with its own tokens was refused: %v", err)
	}

	// The same tokens, without the strict mode: the build goes on, the
	// warning is in the log.
	if err := checkTokens(control, "another-token", digest, false); err != nil {
		t.Fatalf("a builder with the token of the control plane was refused without the strict mode: %v", err)
	}
}

// TestCheckTokensIgnoresAnEmptyFingerprint checks that the platform
// without a fingerprint does not refuse anything.
func TestCheckTokensIgnoresAnEmptyFingerprint(t *testing.T) {
	if err := checkTokens("git-token", "registry-token", "", true); err != nil {
		t.Fatalf("a platform without a fingerprint refused a build: %v", err)
	}
}

// TestTokenDigestIsStableAndDistinct checks that the fingerprint tells
// two tokens apart, and that it is the same for the same token.
func TestTokenDigestIsStableAndDistinct(t *testing.T) {
	first := tokenDigest("ghp_one")
	if first == "" {
		t.Fatal("a token has no fingerprint")
	}
	if first != tokenDigest("ghp_one") {
		t.Fatal("the fingerprint of a token is not stable")
	}
	if first == tokenDigest("ghp_two") {
		t.Fatal("two different tokens have the same fingerprint")
	}
	if tokenDigest("") != "" {
		t.Fatal("an empty token has a fingerprint")
	}
}

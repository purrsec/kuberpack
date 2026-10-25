package platformcli

import "fmt"

// Different strings cannot prove provider-side scopes, but identical tokens
// prove that untrusted build code receives the GitOps writer's authority.
func validateInstallCredentials(c installCredentials) error {
	if c.API != "" && (c.API == c.Git || c.API == c.Registry || c.API == c.BuilderGit) {
		return fmt.Errorf("KUBERPACK_API_TOKEN must be a separate API credential, different from GitOps, registry and builder tokens")
	}
	if c.Git != "" && c.Registry == c.Git {
		return fmt.Errorf("KUBERPACK_REGISTRY_TOKEN must be dedicated to registry push/pull and different from KUBERPACK_GIT_TOKEN; the builder must never receive the GitOps writer")
	}
	if c.BuilderGit != "" && (c.BuilderGit == c.Git || c.BuilderGit == c.Registry) {
		return fmt.Errorf("KUBERPACK_BUILDER_GIT_TOKEN must be a separate read-only source credential, different from the GitOps and registry tokens")
	}
	return nil
}

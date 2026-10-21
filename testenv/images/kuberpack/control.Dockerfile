# Control plane image for the test environment.
#
# Layer on top of the product image: the system trust store is replaced by a
# merged bundle (host CAs + the local test CA) so the control plane can talk
# to the Forgejo API and clone the GitOps repository over HTTPS.
#
# The Chainguard base image has no coreutils, so the bundle is merged on the
# host (pki/ca-bundle-merged.crt) and copied in place.
FROM kuberpack:dev

USER root
COPY pki/ca-bundle-merged.crt /etc/ssl/certs/ca-certificates.crt

USER 65532

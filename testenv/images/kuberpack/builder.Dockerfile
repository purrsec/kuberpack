# Builder image for the test environment.
#
# Layer on top of the product builder image: the system trust store is
# replaced by a merged bundle (host CAs + the local test CA) so `git` (clone
# over HTTPS) and `skopeo` (push to the Forgejo OCI registry) trust the test
# registry certificate.
FROM kuberpack-builder:dev

USER root
COPY pki/ca-bundle-merged.crt /etc/ssl/certs/ca-certificates.crt

USER 1000:1000

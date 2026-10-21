# BuildKit sidecar image for the test environment.
#
# The rootless BuildKit sidecar pushes and pulls the build cache in the Forgejo
# OCI registry over HTTPS: it needs the local test CA in its trust store.
FROM docker.io/moby/buildkit:v0.33.0-rootless

USER root
COPY pki/ca-bundle-merged.crt /etc/ssl/certs/ca-certificates.crt

# Control-plane image. Not the builder: no buildkitd, no privileged Docker.
# Runtime contains only Kuberpack, native Git HTTPS support and its libraries.

# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.9

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/kuberpack-control-plane ./cmd/kuberpack-control-plane && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/kuberpack-reloader ./cmd/kuberpack-reloader

# Git is retained for GitOps; build tools and command interpreters belong only
# in Dockerfile.builder. Copy the HTTPS transport and shared libraries into an
# empty runtime, rather than inheriting the shell/tools of a general git image.
FROM cgr.dev/chainguard/git:latest@sha256:913be6e43cb65ed65bc5c5f9b22e68e780616a66c5237da59334c7d1cdc3d628 AS git-runtime
FROM alpine:3.21 AS runtime-files
COPY --from=git-runtime /usr/lib/ /runtime/usr/lib/
COPY --from=git-runtime /usr/bin/git /runtime/usr/bin/git
COPY --from=git-runtime /usr/libexec/git-core/git-remote-http /runtime/usr/libexec/git-core/git-remote-http
COPY --from=git-runtime /etc/ssl/certs/ca-certificates.crt /runtime/etc/ssl/certs/ca-certificates.crt
RUN mkdir -p /runtime/home/nonroot /runtime/var/lib/kuberpack /runtime/tmp /runtime/usr/share/empty && \
    chmod 1777 /runtime/tmp && \
    chown -R 65532:65532 /runtime/home/nonroot /runtime/var/lib/kuberpack && \
    ln -s usr/lib /runtime/lib && ln -s usr/lib /runtime/lib64 && \
    ln -s git-remote-http /runtime/usr/libexec/git-core/git-remote-https && \
    printf '[safe]\n\tdirectory = *\n[core]\n\thooksPath = /dev/null\n[init]\n\ttemplateDir = /usr/share/empty\n' > /runtime/etc/gitconfig

FROM scratch
COPY --from=runtime-files /runtime/ /
COPY --from=build --chmod=755 /out/kuberpack-control-plane /usr/bin/kuberpack-control-plane
COPY --from=build --chmod=755 /out/kuberpack-reloader /usr/bin/kuberpack-reloader
COPY provisioning/charts/stateless /opt/kuberpack/charts/stateless
COPY provisioning/charts/kuberpack /opt/kuberpack/charts/kuberpack
ENV PATH=/usr/bin \
    HOME=/home/nonroot \
    GIT_EXEC_PATH=/usr/libexec/git-core \
    KUBERPACK_DATA=/var/lib/kuberpack \
    KUBERPACK_CHART=/opt/kuberpack/charts/stateless
USER 65532:65532
WORKDIR /var/lib/kuberpack
EXPOSE 8080
ENTRYPOINT ["/usr/bin/kuberpack-control-plane"]
CMD ["--addr", ":8080"]

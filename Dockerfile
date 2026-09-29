# Control-plane image. Not the builder: no buildkitd, no privileged Docker.
# Runtime is Chainguard git (Wolfi), not Debian. Tool stages are discarded.
# kuberpack/railpack/buildctl/uv are static binaries copied in.

# syntax=docker/dockerfile:1

ARG GO_VERSION=1.23
ARG RAILPACK_VERSION=0.39.0
ARG BUILDKIT_VERSION=v0.23.2
ARG UV_VERSION=0.8.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/kuberpack ./cmd/kuberpack

FROM alpine:3.21 AS railpack
ARG TARGETARCH=amd64
ARG RAILPACK_VERSION
RUN apk add --no-cache ca-certificates curl
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) rarch=x86_64-unknown-linux-musl; rsha=728407f5cdb9e9bc1cdd07f568419344a20e71b0a5a9fd90a9cfbaca0a6c94f7 ;; \
      arm64) rarch=arm64-unknown-linux-musl; rsha=42eb3fa68e38f44be3610a7d74f714ec0d808d70c105fbe35e053b6e6cfb20be ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/railpack.tgz \
      "https://github.com/railwayapp/railpack/releases/download/v${RAILPACK_VERSION}/railpack-v${RAILPACK_VERSION}-${rarch}.tar.gz"; \
    echo "${rsha}  /tmp/railpack.tgz" | sha256sum -c -; \
    tar -xzf /tmp/railpack.tgz -C /tmp; \
    bin="$(find /tmp -type f -name railpack | head -n1)"; \
    install -m0755 "${bin}" /railpack

FROM moby/buildkit:${BUILDKIT_VERSION} AS buildkit
FROM ghcr.io/astral-sh/uv:${UV_VERSION} AS uv

FROM cgr.dev/chainguard/git:latest
USER root
COPY --from=build --chmod=755 /out/kuberpack /usr/bin/kuberpack
COPY --from=railpack --chmod=755 /railpack /usr/bin/railpack
COPY --from=buildkit --chmod=755 /usr/bin/buildctl /usr/bin/buildctl
COPY --from=uv --chmod=755 /uv /usr/bin/uv
COPY charts/stateless /opt/kuberpack/charts/stateless
WORKDIR /var/lib/kuberpack

ENV HOME=/home/nonroot \
    KUBERPACK_DATA=/var/lib/kuberpack \
    KUBERPACK_CHART=/opt/kuberpack/charts/stateless \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0=safe.directory \
    GIT_CONFIG_VALUE_0=*

USER 65532
WORKDIR /var/lib/kuberpack
EXPOSE 8080
ENTRYPOINT ["/usr/bin/kuberpack"]
CMD ["serve", "--addr", ":8080"]

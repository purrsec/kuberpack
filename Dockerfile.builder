# syntax=docker/dockerfile:1
# Builder image for one Kubernetes Job. The application image is produced by
# the rootless BuildKit sidecar, not by this Dockerfile.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS go-build
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
    go build -trimpath -ldflags="-s -w" -o /kuberpack ./cmd/kuberpack

FROM alpine:3.21 AS tools
ARG RAILPACK_VERSION=0.39.0
ARG RAILPACK_SHA256=728407f5cdb9e9bc1cdd07f568419344a20e71b0a5a9fd90a9cfbaca0a6c94f7
ARG TRIVY_VERSION=0.74.0
ARG TRIVY_SHA256=2ae6fe3ee734b7fdf11335663e18c75ea12dccc76062f09f164a3b0f8be4371a
ARG SYFT_VERSION=1.52.0
ARG SYFT_SHA256=caeedb81fb0491615f1ebd1761e4145d41ee86dd2cc7bf80669f9f5ad9d6133d
RUN apk add --no-cache ca-certificates wget tar gzip
RUN set -eu; work="$(mktemp -d)"; \
    wget -qO "$work/railpack.tar.gz" "https://github.com/railwayapp/railpack/releases/download/v${RAILPACK_VERSION}/railpack-v${RAILPACK_VERSION}-x86_64-unknown-linux-musl.tar.gz"; \
    echo "${RAILPACK_SHA256}  $work/railpack.tar.gz" | sha256sum -c -; \
    tar -xzf "$work/railpack.tar.gz" -C "$work"; install -m 0755 "$work/railpack" /railpack; \
    wget -qO "$work/trivy.tar.gz" "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz"; \
    echo "${TRIVY_SHA256}  $work/trivy.tar.gz" | sha256sum -c -; \
    tar -xzf "$work/trivy.tar.gz" -C "$work"; install -m 0755 "$work/trivy" /trivy; \
    wget -qO "$work/syft.tar.gz" "https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/syft_${SYFT_VERSION}_linux_amd64.tar.gz"; \
    echo "${SYFT_SHA256}  $work/syft.tar.gz" | sha256sum -c -; \
    tar -xzf "$work/syft.tar.gz" -C "$work"; install -m 0755 "$work/syft" /syft; \
    rm -rf "$work"

FROM docker.io/moby/buildkit:v0.33.0-rootless@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef AS buildkit

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git skopeo tar bash coreutils nodejs \
    && addgroup -g 1000 kuberpack \
    && adduser -D -u 1000 -G kuberpack kuberpack \
    && mkdir -p /work \
    && chown 1000:1000 /work
COPY --from=go-build --chmod=755 /kuberpack /usr/bin/kuberpack
COPY --from=tools --chmod=755 /railpack /usr/bin/railpack
COPY --from=tools --chmod=755 /trivy /usr/bin/trivy
COPY --from=tools --chmod=755 /syft /usr/bin/syft
COPY --from=buildkit --chmod=755 /usr/bin/buildctl /usr/bin/buildctl
USER 1000:1000
ENV HOME=/work
ENV TMPDIR=/work
WORKDIR /work
ENTRYPOINT ["/usr/bin/kuberpack", "builder"]

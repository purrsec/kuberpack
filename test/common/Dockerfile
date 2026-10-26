# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS go
FROM docker:29.9.2-cli@sha256:5e6baf67325a79f64ed3bed6863954d5e05d0f3d4e57cbdb14809346814bbfdb AS docker
FROM python:3.13.17-slim-bookworm@sha256:e32be0e55814a08977501ca863271d9bd02443710db8f2afaf8372e2c4508530
COPY --from=go /usr/local/go /usr/local/go
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    GOTOOLCHAIN=auto GOCACHE=/cache/build GOPATH=/cache/go \
    PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    ANSIBLE_CONFIG=/workspace/test/common/ansible.cfg
RUN apt-get update && apt-get install -y --no-install-recommends \
      git gcc libc6-dev ca-certificates curl tar gzip openssl iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt collections.yaml /tmp/tools/
RUN pip install --no-cache-dir -r /tmp/tools/requirements.txt \
    && ansible-galaxy collection install -r /tmp/tools/collections.yaml
ARG KUBECTL_VERSION=v1.37.1
ARG HELM_VERSION=v3.19.0
RUN curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
    && curl -fsSLo /tmp/kubectl.sha256 "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256" \
    && printf '%s  /usr/local/bin/kubectl\n' "$(cat /tmp/kubectl.sha256)" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/kubectl \
    && curl -fsSLo /tmp/helm-${HELM_VERSION}-linux-amd64.tar.gz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" \
    && curl -fsSLo /tmp/helm.sha256 "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz.sha256sum" \
    && cd /tmp && sha256sum -c helm.sha256 \
    && tar -xzf /tmp/helm-${HELM_VERSION}-linux-amd64.tar.gz -C /tmp \
    && install -m 0755 /tmp/linux-amd64/helm /usr/local/bin/helm \
    && rm -rf /tmp/linux-amd64 /tmp/helm* /tmp/kubectl.sha256
RUN git config --system safe.directory /workspace
RUN curl -fsSLo /tmp/infisical.tar.gz https://github.com/Infisical/cli/releases/download/v0.43.140/cli_0.43.140_linux_amd64.tar.gz \
    && printf 'c9f014c78cb1b4ba57f758f921843dbe541a3c8d2b83e622b0dc67d77ead4178  /tmp/infisical.tar.gz\n' | sha256sum -c - \
    && tar -xzf /tmp/infisical.tar.gz -C /tmp \
    && install -m 0755 /tmp/infisical /usr/local/bin/infisical \
    && rm -f /tmp/infisical /tmp/infisical.tar.gz
RUN --mount=type=cache,target=/cache/go \
    --mount=type=cache,target=/cache/build \
    GOBIN=/usr/local/bin go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
WORKDIR /workspace
ENTRYPOINT ["python3", "/workspace/test/common/suite.py"]

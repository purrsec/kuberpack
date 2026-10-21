# k3s node for the Kuberpack test environment.
#
# Plain Debian base + the k3s single binary (no systemd inside the container:
# the entrypoint runs `k3s server` directly). The local test CA is added to the
# system trust store so containerd can pull images from the Forgejo registry
# over HTTPS.
FROM debian:bookworm-slim

ARG K3S_VERSION=v1.37.1+k3s1
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl iptables ipset conntrack socat ethtool iproute2 \
        procps jq tar gzip xz-utils \
    && rm -rf /var/lib/apt/lists/*

RUN enc="$(printf '%s' "${K3S_VERSION}" | sed 's/+/%2B/')" \
    && curl -fsSL -o /tmp/k3s \
        "https://github.com/k3s-io/k3s/releases/download/${enc}/k3s" \
    && install -m 0755 /tmp/k3s /usr/local/bin/k3s \
    && rm -f /tmp/k3s \
    && k3s --version

# containerd (the CRI runtime of k3s) resolves registry certificates through
# the system trust store of this container.
COPY pki/ca.crt /usr/local/share/ca-certificates/kuberpack-test-ca.crt
RUN update-ca-certificates

COPY images/k3s/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

ENV K3S_DATA_DIR=/var/lib/rancher/k3s
EXPOSE 6443 80 443
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

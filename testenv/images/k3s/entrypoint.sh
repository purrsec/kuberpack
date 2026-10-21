#!/usr/bin/env bash
# Entrypoint for the k3s node of the test environment.
#
# k3s is started directly (no systemd inside the container). Everything is
# driven by environment variables so the node can be recreated easily.
set -euo pipefail

DATA_DIR="${K3S_DATA_DIR:-/var/lib/rancher/k3s}"
NODE_IP="${K3S_NODE_IP:-172.29.90.11}"
NODE_NAME="${K3S_NODE_NAME:-kuberpack-test}"
WRITE_KUBECONFIG_MODE="${K3S_WRITE_KUBECONFIG_MODE:-644}"

mkdir -p "${DATA_DIR}"

args=(
    server
    --data-dir "${DATA_DIR}"
    --node-name "${NODE_NAME}"
    --node-ip "${NODE_IP}"
    --node-external-ip "${NODE_IP}"
    --advertise-address "${NODE_IP}"
    --tls-san "${NODE_IP}"
    --tls-san forgejo
    --tls-san k3s
    --tls-san localhost
    --flannel-iface eth0
    --write-kubeconfig-mode "${WRITE_KUBECONFIG_MODE}"
    --kubelet-arg=cgroup-driver=cgroupfs
    "--kubelet-arg=eviction-hard=imagefs.available<5%"
    "--kubelet-arg=eviction-hard=nodefs.available<5%"
    "--kubelet-arg=eviction-hard=memory.available<200Mi"
    --disable=metrics-server
)

# Optional: extra arguments (comma separated) for experiments.
if [ -n "${K3S_EXTRA_ARGS:-}" ]; then
    IFS=',' read -ra extra <<<"${K3S_EXTRA_ARGS}"
    args+=("${extra[@]}")
fi

echo "k3s: starting server (node-ip ${NODE_IP})"
echo "k3s: args ${args[*]}"
exec k3s "${args[@]}"

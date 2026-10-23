#!/usr/bin/env bash
# Prepare a real server for Kuberpack: a Kubernetes cluster, Flux, the
# control plane and the TLS the forge needs for its webhooks.
#
# It runs on the server, as root. It is idempotent.
#
#   ./cloud-bootstrap.sh \
#     --host kuberpack.203.0.113.10.nip.io \
#     --git-provider github \
#     --git-url https://api.github.com \
#     --git-user octocat \
#     --git-token ghp_xxx \
#     --gitops-url https://github.com/octocat/gitops.git \
#     --registry-url ghcr.io \
#     --registry-user octocat \
#     --registry-token ghp_xxx
set -euo pipefail

HOST=""
GIT_PROVIDER="github"
GIT_URL="https://api.github.com"
GIT_USER=""
GIT_TOKEN=""
GITOPS_URL=""
GITOPS_BRANCH="main"
REGISTRY_URL=""
REGISTRY_USER=""
REGISTRY_TOKEN=""
REPO_DIR="${REPO_DIR:-/opt/kuberpack}"
KUBERPACK_VERSION="${KUBERPACK_VERSION:-latest}"

while [ $# -gt 0 ]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --git-provider) GIT_PROVIDER="$2"; shift 2 ;;
    --git-url) GIT_URL="$2"; shift 2 ;;
    --git-user) GIT_USER="$2"; shift 2 ;;
    --git-token) GIT_TOKEN="$2"; shift 2 ;;
    --gitops-url) GITOPS_URL="$2"; shift 2 ;;
    --gitops-branch) GITOPS_BRANCH="$2"; shift 2 ;;
    --registry-url) REGISTRY_URL="$2"; shift 2 ;;
    --registry-user) REGISTRY_USER="$2"; shift 2 ;;
    --registry-token) REGISTRY_TOKEN="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$HOST" ] || [ -z "$GIT_TOKEN" ] || [ -z "$GITOPS_URL" ]; then
  echo "--host, --git-token and --gitops-url are required" >&2
  exit 2
fi

log() { printf '\n== %s\n' "$*"; }

log "the cluster"
if ! command -v k3s >/dev/null 2>&1; then
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server --disable traefik" sh -
else
  echo "k3s is already installed"
fi
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl get nodes -o wide

log "the namespace and the secrets of Kuberpack"
kubectl create namespace kuberpack --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace kuberpack-build --dry-run=client -o yaml | kubectl apply -f -

log "Flux"
kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
kubectl -n flux-system wait --timeout=180s --for=condition=Ready kustomization/flux-system

log "cert-manager, for the webhook the forge calls"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.2/cert-manager.yaml
kubectl -n cert-manager wait --timeout=180s --for=condition=Available deployment/cert-manager-webhook
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: kuberpack
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@${HOST}
    privateKeySecretRef:
      name: letsencrypt-account
    solvers:
      - http01:
          ingress:
            name: kuberpack
            class: traefik
EOF

log "the control plane"
helm repo add kuberpack https://purrsec.github.io/kuberpack 2>/dev/null || true
helm upgrade --install kuberpack "${REPO_DIR}/provisioning/charts/kuberpack" \
  --namespace kuberpack \
  --set image="ghcr.io/${REGISTRY_USER:-purrsec}/kuberpack:${KUBERPACK_VERSION}" \
  --set builderImage="ghcr.io/${REGISTRY_USER:-purrsec}/kuberpack-builder:${KUBERPACK_VERSION}" \
  --set gitProvider="${GIT_PROVIDER}" \
  --set gitURL="${GIT_URL}" \
  --set gitopsURL="${GITOPS_URL}" \
  --set gitopsBranch="${GITOPS_BRANCH}" \
  --set registryURL="${REGISTRY_URL}" \
  --set registryUser="${REGISTRY_USER}" \
  --set host="${HOST}" \
  --set webhookURL="https://${HOST}/hooks/git" \
  --wait

log "the TLS of the control plane"
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kuberpack
  namespace: kuberpack
spec:
  secretName: kuberpack-tls
  dnsNames:
    - ${HOST}
  issuerRef:
    name: letsencrypt
    kind: Issuer
EOF

log "the certificate"
kubectl -n kuberpack wait --timeout=300s --for=condition=Ready certificate/kuberpack

log "the secrets"
kubectl -n kuberpack create secret generic kuberpack \
  --from-literal=KUBERPACK_GIT_TOKEN="${GIT_TOKEN}" \
  --from-literal=KUBERPACK_API_TOKEN="${GIT_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kuberpack-build create secret generic kuberpack-builder \
  --from-literal=git-token="${GIT_TOKEN}" \
  --from-literal=registry-token="${REGISTRY_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -
if [ -n "$REGISTRY_TOKEN" ] && [ -n "$REGISTRY_USER" ]; then
  kubectl -n kuberpack-build create secret docker-registry kuberpack-builder-pull \
    --docker-server="${REGISTRY_URL}" \
    --docker-username="${REGISTRY_USER}" \
    --docker-password="${REGISTRY_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -
fi

log "the Flux resources that watch the GitOps repository"
kubectl -n kuberpack create secret generic kuberpack-gitops \
  --from-literal=username="${GIT_USER:-x-access-token}" \
  --from-literal=password="${GIT_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -
cat <<EOF | kubectl apply -f -
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: gitops
  namespace: kuberpack
spec:
  interval: 1m
  url: ${GITOPS_URL}
  ref:
    branch: ${GITOPS_BRANCH}
  secretRef:
    name: kuberpack-gitops
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: kuberpack-projects
  namespace: kuberpack
spec:
  interval: 1m
  path: ./kubernetes/vps/projects
  prune: true
  sourceRef:
    kind: GitRepository
    name: gitops
EOF

log "done"
echo "the control plane answers on https://${HOST}/healthz"
echo "the webhook of the forge is https://${HOST}/hooks/git"

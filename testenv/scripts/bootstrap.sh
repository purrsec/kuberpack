#!/usr/bin/env bash
# Recreate the whole Kuberpack test environment.
#
# Idempotent: the Forgejo data and the k3s data live in Docker volumes, and
# the GitOps repository keeps its content. Everything else is rebuilt.
#
#   ./scripts/bootstrap.sh          # create what is missing
#   ./scripts/bootstrap.sh --reset  # also drop the volumes and start over
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV="$(dirname "${HERE}")"
REPO="$(dirname "${TESTENV}")"
cd "${TESTENV}"

RESET=0
[ "${1:-}" = "--reset" ] && RESET=1

export PATH="${HOME}/.local/bin:${PATH}"
export KUBECONFIG="${HOME}/.kube/k3s-test.yaml"

NETWORK=kuberpack-test
SUBNET=172.29.90.0/24
FORGEJO_IP=172.29.90.10
K3S_IP=172.29.90.11
FIXTURE_API="https://kuberpack.${K3S_IP}.nip.io"

log() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

# ---------------------------------------------------------------- prerequisites
log "prerequisites"
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker daemon is not running" >&2; exit 1; }
for tool in kubectl helm flux git openssl; do
    if ! command -v "${tool}" >/dev/null; then
        echo "missing tool: ${tool} (kubectl/helm/flux go in ~/.local/bin)" >&2
        exit 1
    fi
done

# ------------------------------------------------------------------- PKI
log "PKI"
mkdir -p pki
if [ ! -f pki/ca.crt ]; then
    openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
        -keyout pki/ca.key -out pki/ca.crt \
        -subj "/CN=kuberpack-test-CA/O=kuberpack-test" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
    cat >pki/san.cnf <<'EOF'
[req]
distinguished_name = dn
req_extensions = v3_req
prompt = no
[dn]
CN = forgejo.kuberpack.test
O = kuberpack-test
[v3_req]
basicConstraints = CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt
[alt]
DNS.1 = forgejo
DNS.2 = forgejo.kuberpack.test
DNS.3 = localhost
IP.1 = 172.29.90.10
IP.2 = 127.0.0.1
EOF
    openssl req -newkey rsa:2048 -nodes -keyout pki/forgejo.key -out pki/forgejo.csr -config pki/san.cnf 2>/dev/null
    openssl x509 -req -in pki/forgejo.csr -CA pki/ca.crt -CAkey pki/ca.key -CAcreateserial \
        -out pki/forgejo.crt -days 3650 -sha256 -extensions v3_req -extfile pki/san.cnf 2>/dev/null
    chmod 600 pki/ca.key pki/forgejo.key
    echo "CA and certificate created"
else
    echo "PKI already present"
fi
# Merged trust store: host CAs + local CA (used by the Kuberpack images).
cat /etc/ssl/certs/ca-certificates.crt pki/ca.crt >pki/ca-bundle-merged.crt

# ------------------------------------------------------------------- network
log "docker network"
if ! docker network inspect "${NETWORK}" >/dev/null 2>&1; then
    docker network create --driver bridge --subnet "${SUBNET}" "${NETWORK}" >/dev/null
    echo "network ${NETWORK} created"
else
    echo "network ${NETWORK} already present"
fi

# ------------------------------------------------------------------- images
log "images"
docker build -q -f images/forgejo/Dockerfile -t kuberpack-test/forgejo:17 . >/dev/null
docker build -q -f images/k3s/Dockerfile -t kuberpack-test/k3s:v1.37.1 . >/dev/null
(cd "${REPO}" && docker build -q -f Dockerfile -t kuberpack:dev . >/dev/null)
(cd "${REPO}" && docker build -q -f Dockerfile.builder -t kuberpack-builder:dev . >/dev/null)
docker build -q -f images/kuberpack/control.Dockerfile -t kuberpack-test/control:dev . >/dev/null
docker build -q -f images/kuberpack/builder.Dockerfile -t kuberpack-test/builder:dev . >/dev/null
docker build -q -f images/kuberpack/buildkit.Dockerfile -t kuberpack-test/buildkit:dev . >/dev/null
echo "images built"

# ------------------------------------------------------------------- forgejo
log "forgejo container"
if [ "${RESET}" = "1" ]; then
    docker rm -f forgejo >/dev/null 2>&1 || true
    docker volume rm -f kuberpack-forgejo-data >/dev/null 2>&1 || true
fi
if [ ! -f .forgejo-secrets.env ]; then
    echo "SECRET_KEY=$(openssl rand -hex 32)" >.forgejo-secrets.env
    echo "INTERNAL_TOKEN=$(openssl rand -hex 32)" >>.forgejo-secrets.env
    chmod 600 .forgejo-secrets.env
fi
docker volume create kuberpack-forgejo-data >/dev/null
if ! docker ps --format '{{.Names}}' | grep -qx forgejo; then
    docker rm -f forgejo >/dev/null 2>&1 || true
    docker run -d --name forgejo \
        --network "${NETWORK}" --ip "${FORGEJO_IP}" \
        -p 3000:3000 -p 2222:22 \
        -v kuberpack-forgejo-data:/var/lib/forgejo \
        -v "${TESTENV}/pki:/etc/forgejo/tls:ro" \
        -e FORGEJO_DOMAIN="${FORGEJO_IP}" \
        -e FORGEJO_ROOT_URL="https://${FORGEJO_IP}:3000" \
        -e FORGEJO_SSH_PORT=2222 \
        -e FORGEJO_SECRET_KEY="$(grep SECRET_KEY .forgejo-secrets.env | cut -d= -f2)" \
        -e FORGEJO_INTERNAL_TOKEN="$(grep INTERNAL_TOKEN .forgejo-secrets.env | cut -d= -f2)" \
        -e FORGEJO_ALLOWED_HOST_LIST="loopback, private, 172.29.90.0/24" \
        --restart unless-stopped \
        kuberpack-test/forgejo:17 >/dev/null
    echo "forgejo started"
fi
for i in $(seq 1 40); do
    curl -sS --cacert pki/ca.crt --max-time 5 "https://${FORGEJO_IP}:3000/api/v1/version" >/dev/null 2>&1 && break
    sleep 3
done

# ------------------------------------------------------------------- k3s
log "k3s container"
if [ "${RESET}" = "1" ]; then
    docker rm -f k3s >/dev/null 2>&1 || true
    docker volume rm -f kuberpack-k3s-data >/dev/null 2>&1 || true
fi
docker volume create kuberpack-k3s-data >/dev/null
if ! docker ps --format '{{.Names}}' | grep -qx k3s; then
    docker rm -f k3s >/dev/null 2>&1 || true
    docker run -d --name k3s \
        --privileged --cgroupns=host \
        --network "${NETWORK}" --ip "${K3S_IP}" \
        -p 6443:6443 -p 80:80 -p 443:443 \
        -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
        -v kuberpack-k3s-data:/var/lib/rancher/k3s \
        --tmpfs /var/lib/kubelet:exec,size=20g \
        -e K3S_NODE_IP="${K3S_IP}" -e K3S_NODE_NAME=kuberpack-test \
        --restart unless-stopped \
        kuberpack-test/k3s:v1.37.1 >/dev/null
    echo "k3s started"
fi
mkdir -p "${HOME}/.kube"
for i in $(seq 1 60); do
    docker exec k3s k3s kubectl get node kuberpack-test >/dev/null 2>&1 && break
    sleep 5
done
docker exec k3s cat /etc/rancher/k3s/k3s.yaml > /tmp/k3s.yaml
sed -i "s#https://127.0.0.1:6443#https://${K3S_IP}:6443#g" /tmp/k3s.yaml
install -m 600 /tmp/k3s.yaml "${KUBECONFIG}"
echo "kubeconfig written to ${KUBECONFIG}"

log "importing the Kuberpack images into the k3s containerd"
docker save kuberpack-test/control:dev kuberpack-test/builder:dev kuberpack-test/buildkit:dev \
    | docker exec -i k3s k3s ctr -n k8s.io images import - >/dev/null
echo "images imported"

# ------------------------------------------------------------------- forgejo account
log "forgejo account, tokens and repositories"
CTL_TOKEN=""
ADMIN_PASSWORD="${FORGEJO_ADMIN_PASSWORD:-}"
if [ -z "${ADMIN_PASSWORD}" ] && [ -f .env.test ]; then
    ADMIN_PASSWORD=$(sed -n 's/^FORGEJO_ADMIN_PASSWORD=//p' .env.test)
fi
if [ -z "${ADMIN_PASSWORD}" ]; then
    ADMIN_PASSWORD=$(openssl rand -hex 24)
fi
if [ -f .env.test ]; then
    CTL_TOKEN=$(grep FORGEJO_CONTROL_TOKEN .env.test | cut -d= -f2)
fi
if [ -z "${CTL_TOKEN}" ] || ! curl -sS --cacert pki/ca.crt -H "Authorization: token ${CTL_TOKEN}" \
        "https://${FORGEJO_IP}:3000/api/v1/user" | grep -q login; then
    docker exec forgejo forgejo admin user create --config /etc/forgejo/app.ini \
        --admin --username kuberpack --password "${ADMIN_PASSWORD}" \
        --email kuberpack@test.local --must-change-password=false >/dev/null 2>&1 || true
    docker exec forgejo forgejo admin user create --config /etc/forgejo/app.ini \
        --username kuberpack-builder --password "$(openssl rand -hex 24)" \
        --email builder@test.local --must-change-password=false >/dev/null 2>&1 || true
    CTL_TOKEN=$(docker exec forgejo forgejo admin user generate-access-token --config /etc/forgejo/app.ini \
        --username kuberpack --token-name "kuberpack-control-$(date +%s)" --raw \
        --scopes write:repository 2>/dev/null | tail -1 | tr -d '\r\n')
    BUILDER_TOKEN=$(docker exec forgejo forgejo admin user generate-access-token --config /etc/forgejo/app.ini \
        --username kuberpack-builder --token-name "builder-git-$(date +%s)" --raw \
        --scopes read:repository 2>/dev/null | tail -1 | tr -d '\r\n')
    REGISTRY_TOKEN=$(docker exec forgejo forgejo admin user generate-access-token --config /etc/forgejo/app.ini \
        --username kuberpack --token-name "kuberpack-registry-$(date +%s)" --raw \
        --scopes write:package,read:package 2>/dev/null | tail -1 | tr -d '\r\n')
    cat >.env.test <<EOF
# Kuberpack test environment - secrets (do not commit)
FORGEJO_URL=https://${FORGEJO_IP}:3000
FORGEJO_HOST=${FORGEJO_IP}
FORGEJO_PORT=3000
FORGEJO_OWNER=kuberpack
FORGEJO_CONTROL_TOKEN=${CTL_TOKEN}
FORGEJO_BUILDER_TOKEN=${BUILDER_TOKEN}
FORGEJO_REGISTRY_TOKEN=${REGISTRY_TOKEN}
FORGEJO_ADMIN_USER=kuberpack
FORGEJO_ADMIN_PASSWORD=${ADMIN_PASSWORD}
GITOPS_REPO=kuberpack/gitops
APP_REPO=kuberpack/hello-kuberpack
BUILDS_REPO=kuberpack/kuberpack-builds
K3S_API=https://${K3S_IP}:6443
K3S_NODE_IP=${K3S_IP}
EOF
    chmod 600 .env.test
    echo "tokens written to .env.test"
fi
set -a; . ./.env.test; set +a

for repo in gitops hello-kuberpack kuberpack-builds; do
    curl -sS --cacert pki/ca.crt -H "Authorization: token ${FORGEJO_CONTROL_TOKEN}" \
        "https://${FORGEJO_IP}:3000/api/v1/repos/kuberpack/${repo}" >/dev/null 2>&1 || \
    curl -sS --cacert pki/ca.crt -H "Authorization: token ${FORGEJO_CONTROL_TOKEN}" \
        -H "Content-Type: application/json" -X POST "https://${FORGEJO_IP}:3000/api/v1/user/repos" \
        -d "{\"name\":\"${repo}\",\"private\":false,\"default_branch\":\"main\",\"auto_init\":true}" >/dev/null
    echo "repository ${repo} ready"
done

# ------------------------------------------------------------------- gitops
log "gitops repository content"
WORK=$(mktemp -d)
git clone -q "https://kuberpack:${FORGEJO_CONTROL_TOKEN}@${FORGEJO_IP}:3000/${GITOPS_REPO}.git" "${WORK}/repo"
rsync -a --delete --exclude '.git' gitops/ "${WORK}/repo/"
(cd "${WORK}/repo" && git config user.email "kuberpack@test.local" && git config user.name "Kuberpack Test" \
    && git add -A && git diff --cached --quiet || git commit -q -m "gitops: update cluster layout" && git push -q origin main)
rm -rf "${WORK}"
echo "gitops pushed"

# ------------------------------------------------------------------- flux
log "flux"
flux install --namespace=flux-system \
    --components-extra=image-reflector-controller,image-automation-controller >/dev/null
kubectl -n flux-system create configmap ca-bundle \
    --from-file=ca-bundle.crt=pki/ca-bundle-merged.crt --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n flux-system patch deployment source-controller --type=strategic -p '{
  "spec":{"template":{"spec":{
    "containers":[{"name":"manager",
      "env":[{"name":"SSL_CERT_FILE","value":"/etc/kuberpack/ca/ca-bundle.crt"}],
      "volumeMounts":[{"name":"ca-bundle","mountPath":"/etc/kuberpack/ca","readOnly":true}]}],
    "volumes":[{"name":"ca-bundle","configMap":{"name":"ca-bundle"}}]
  }}}}' >/dev/null
kubectl -n flux-system rollout status deployment/source-controller --timeout=180s >/dev/null
kubectl -n flux-system create secret generic gitops-ca --from-file=caFile=pki/ca.crt \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n flux-system create secret generic gitops-auth \
    --from-literal=username=kuberpack --from-literal=password="${FORGEJO_CONTROL_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cat <<'EOF' | kubectl apply -f - >/dev/null
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: infra-homelab
  namespace: flux-system
spec:
  interval: 1m
  url: https://172.29.90.10:3000/kuberpack/gitops.git
  ref:
    branch: main
  secretRef:
    name: gitops-auth
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-homelab
  namespace: flux-system
spec:
  interval: 1m
  path: ./kubernetes/vps/apps
  prune: true
  wait: false
  sourceRef:
    kind: GitRepository
    name: infra-homelab
---
# The projects are a second tree: the manifest of a project and the artifacts
# compiled from it live in kubernetes/vps/projects. Flux needs one
# Kustomization per tree, otherwise a compiled project is never applied.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-homelab-projects
  namespace: flux-system
spec:
  interval: 1m
  path: ./kubernetes/vps/projects
  prune: true
  wait: false
  sourceRef:
    kind: GitRepository
    name: infra-homelab
EOF
echo "flux installed"

# ------------------------------------------------------------------- cnpg
# The postgres addon of a project is a CloudNativePG cluster: the operator
# must run in the cluster. The CRDs are too big for a client side apply.
log "cloudnativepg operator"
CNPG_VERSION="${CNPG_VERSION:-1.30.1}"
curl -sSL --max-time 120 -o /tmp/cnpg-operator.yaml \
    "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v${CNPG_VERSION}/cnpg-${CNPG_VERSION}.yaml"
kubectl apply --server-side --force-conflicts -f /tmp/cnpg-operator.yaml >/dev/null
kubectl -n cnpg-system wait --for=condition=Ready pod \
    --selector=cnpg.io/operator=true --timeout=240s >/dev/null
echo "cloudnativepg installed"

# ------------------------------------------------------------------- control plane
log "kuberpack control plane"
./scripts/deploy-kuberpack.sh
export KUBERPACK_TEST_ADMIN_TOKEN="$(kubectl -n kuberpack get secret kuberpack -o jsonpath='{.data.KUBERPACK_API_TOKEN}' | base64 -d)"
(cd "${REPO}" && make build)


# ------------------------------------------------------------------- app
log "test application"
./scripts/push-app.sh "app: hello-kuberpack FastAPI service with uv lockfile" || true
# The manifest is the only way to declare an application: the control plane
# compiles it, arms the build and lets Flux apply the result.
if ! curl -sS --max-time 20 --cacert pki/ca.crt -H "Authorization: Bearer ${KUBERPACK_TEST_ADMIN_TOKEN}" \
        "${FIXTURE_API}/api/v1/apps" | grep -q "demo-hello-kuberpack"; then
    cat > /tmp/kuberpack-demo-manifest.yaml <<'MANIFEST'
project: demo
namespace: demo

services:
  hello-kuberpack:
    type: stateless
    repository: kuberpack/hello-kuberpack
    branch: main
    strategy: auto
    port: 8080
    hostname: hello-kuberpack.kuberpack.test
    healthcheck: /healthz
    build:
      start_command: uvicorn main:app --host 0.0.0.0 --port $PORT

policies:
  defaultDeny: true
  internet: true
MANIFEST
    python3 scripts/_cli_v2.py --project demo --file /tmp/kuberpack-demo-manifest.yaml \
        --api "${FIXTURE_API}" --ca-file pki/ca.crt \
        --repository kuberpack/hello-kuberpack --domain hello-kuberpack.kuberpack.test >/dev/null
    echo "application declared by the manifest"
fi

log "done"
echo "Control plane : ${FIXTURE_API}  (dedicated API credential in its Kubernetes Secret)"
echo "Forgejo       : https://${FORGEJO_IP}:3000"
echo "Application   : curl -H 'Host: hello-kuberpack.kuberpack.test' http://${K3S_IP}/"

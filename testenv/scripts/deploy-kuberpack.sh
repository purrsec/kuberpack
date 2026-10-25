#!/usr/bin/env bash
# Deploy the Kuberpack control plane in the test cluster.
#
# Creates the namespaces, the secrets and installs the product chart with the
# values of the test environment.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV="$(dirname "${HERE}")"
REPO="$(dirname "${TESTENV}")"

# shellcheck disable=SC1091
set -a; . "${TESTENV}/.env.test"; set +a
export KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/k3s-test.yaml}"
export PATH="${HOME}/.local/bin:${PATH}"

echo "== namespaces =="
kubectl create namespace kuberpack --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace kuberpack-build --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace apps --dry-run=client -o yaml | kubectl apply -f -
# The chart owns the kuberpack-build namespace: let Helm adopt it.
kubectl label namespace kuberpack-build app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate namespace kuberpack-build \
    meta.helm.sh/release-name=kuberpack \
    meta.helm.sh/release-namespace=kuberpack --overwrite

echo "== secrets =="
# The API administrator secret is distinct from every forge credential.
fixture_api_token="$(kubectl -n kuberpack get secret kuberpack -o jsonpath='{.data.KUBERPACK_API_TOKEN}' 2>/dev/null | base64 -d || true)"
if [ -z "${fixture_api_token}" ] || [ "${fixture_api_token}" = "${FORGEJO_CONTROL_TOKEN}" ]; then
    fixture_api_token="$(openssl rand -hex 32)"
fi
kubectl -n kuberpack create secret generic kuberpack \
    --from-literal=FORGEJO_TOKEN="${FORGEJO_CONTROL_TOKEN}" \
    --from-literal=KUBERPACK_API_TOKEN="${fixture_api_token}" \
    --from-literal=KUBERPACK_REGISTRY_TOKEN="${FORGEJO_REGISTRY_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -

# Builder: a dedicated Git token and a registry token (never the control plane
# PAT), as the deployment contract requires.
kubectl -n kuberpack-build create secret generic kuberpack-builder \
    --from-literal=git-token="${FORGEJO_BUILDER_TOKEN}" \
    --from-literal=registry-token="${FORGEJO_REGISTRY_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -

# Pull secret for the builder images and for the application images.
for ns in kuberpack-build apps; do
    kubectl -n "${ns}" create secret docker-registry forgejo-registry-pull \
        --docker-server="${FORGEJO_HOST}:${FORGEJO_PORT}" \
        --docker-username="${FORGEJO_OWNER}" \
        --docker-password="${FORGEJO_REGISTRY_TOKEN}" \
        --dry-run=client -o yaml | kubectl apply -f -
done
kubectl -n kuberpack-build create secret docker-registry kuberpack-builder-pull \
    --docker-server="${FORGEJO_HOST}:${FORGEJO_PORT}" \
    --docker-username="${FORGEJO_OWNER}" \
    --docker-password="${FORGEJO_REGISTRY_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "== fixture API TLS =="
fixture_api_host="kuberpack.${K3S_NODE_IP}.nip.io"
openssl req -new -newkey rsa:2048 -nodes -keyout "${TESTENV}/pki/control.key" \
    -out "${TESTENV}/pki/control.csr" -subj "/CN=${fixture_api_host}" >/dev/null 2>&1
printf 'subjectAltName=DNS:%s\n' "${fixture_api_host}" >"${TESTENV}/pki/control.ext"
openssl x509 -req -in "${TESTENV}/pki/control.csr" -CA "${TESTENV}/pki/ca.crt" \
    -CAkey "${TESTENV}/pki/ca.key" -CAcreateserial -days 30 \
    -extfile "${TESTENV}/pki/control.ext" -out "${TESTENV}/pki/control.crt" >/dev/null 2>&1
chmod 600 "${TESTENV}/pki/control.key"
kubectl -n kuberpack create secret tls kuberpack-test-tls \
    --cert="${TESTENV}/pki/control.crt" --key="${TESTENV}/pki/control.key" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "== helm install =="
helm upgrade --install kuberpack "${REPO}/provisioning/charts/kuberpack" \
    --namespace kuberpack \
    --set image=docker.io/kuberpack-test/control:dev \
    --set builderImage=docker.io/kuberpack-test/builder:dev \
    --set gitURL="${FORGEJO_URL}" \
    --set gitopsURL="${FORGEJO_URL}/${GITOPS_REPO}.git" \
    --set gitopsApps=kubernetes/vps/apps \
    --set gitopsChart=./kubernetes/vps/charts/stateless \
    --set gitopsSource=infra-homelab \
    --set previewDomain=preview.kuberpack.test \
    --set host="${fixture_api_host}" \
    --set webhookURL="https://${fixture_api_host}/hooks/git" \
    --set ingress.enabled=true \
    --set ingress.tls=true \
    --set tlsSecret=kuberpack-test-tls \
    --set buildNetworkPolicy.enabled=false \
    --set wait=45m \
    --set-string 'extraEnv[0].name=KUBERPACK_BUILDKIT_IMAGE' \
    --set-string 'extraEnv[0].value=docker.io/kuberpack-test/buildkit:dev' \
    --set-string 'extraEnv[1].name=KUBERPACK_INGRESS_TLS' \
    --set-string 'extraEnv[1].value=false' \
    --set-string 'extraEnv[2].name=KUBERPACK_TRAEFIK_ENTRYPOINT' \
    --set-string 'extraEnv[2].value=web' \
    --set-string 'extraEnv[3].name=KUBERPACK_TRAEFIK_CERT_RESOLVER' \
    --set-string 'extraEnv[3].value=' \
    --wait --timeout 5m

echo "== control plane =="
# The image tag does not change between local rebuilds, so the pod template is
# unchanged: force a rollout to pick up the new image.
kubectl -n kuberpack rollout restart deployment/kuberpack
kubectl -n kuberpack rollout status deployment/kuberpack --timeout=180s
kubectl -n kuberpack get pods,svc

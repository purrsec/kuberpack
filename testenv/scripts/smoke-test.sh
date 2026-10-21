#!/usr/bin/env bash
# End-to-end check of the Kuberpack test environment.
#
# Verifies every link of the chain:
#   Forgejo -> webhook -> build -> digest -> GitOps -> Flux -> application
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV="$(dirname "${HERE}")"
cd "${TESTENV}"

set -a; . ./.env.test; set +a
export KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/k3s-test.yaml}"

ok=0
fail=0
check() {
    local label="$1" expected="$2" actual="$3"
    if [ "${actual}" = "${expected}" ]; then
        printf '  \033[32mok\033[0m   %-46s %s\n' "${label}" "${actual}"
        ok=$((ok + 1))
    else
        printf '  \033[31mFAIL\033[0m %-46s got=%s want=%s\n' "${label}" "${actual}" "${expected}"
        fail=$((fail + 1))
    fi
}

echo "== conteneurs =="
check "forgejo running" "true" "$([ "$(docker inspect -f '{{.State.Running}}' forgejo 2>/dev/null)" = "true" ] && echo true || echo false)"
check "k3s running" "true" "$([ "$(docker inspect -f '{{.State.Running}}' k3s 2>/dev/null)" = "true" ] && echo true || echo false)"

echo "== cluster =="
check "node ready" "True" "$(kubectl get node kuberpack-test -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)"
check "flux gitrepository ready" "True" "$(kubectl -n flux-system get gitrepository infra-homelab -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)"
check "flux kustomization ready" "True" "$(kubectl -n flux-system get kustomization infra-homelab -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)"
check "flux projects kustomization ready" "True" "$(kubectl -n flux-system get kustomization infra-homelab-projects -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)"
check "control plane ready" "True" "$(kubectl -n kuberpack get deploy kuberpack -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)"

echo "== forgejo =="
check "api version" "17.0.0" "$(curl -sS --cacert pki/ca.crt --max-time 10 https://${FORGEJO_HOST}:${FORGEJO_PORT}/api/v1/version 2>/dev/null | jq -r '.version | split("+")[0]')"
check "oci registry enabled" "401" "$(curl -sS --cacert pki/ca.crt -o /dev/null -w '%{http_code}' --max-time 10 https://${FORGEJO_HOST}:${FORGEJO_PORT}/v2/ 2>/dev/null)"
check "webhook configured" "1" "$(curl -sS --cacert pki/ca.crt -H "Authorization: token ${FORGEJO_CONTROL_TOKEN}" --max-time 10 "https://${FORGEJO_HOST}:${FORGEJO_PORT}/api/v1/repos/kuberpack/hello-kuberpack/hooks" 2>/dev/null | jq -r 'length')"

echo "== kuberpack =="
check "apps declared by the manifests" "3" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps" 2>/dev/null | jq -r '.apps | length')"
check "app belongs to the project" "demo" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps" 2>/dev/null | jq -r '.apps[] | select(.name == "demo-hello-kuberpack") | .project')"
check "digest targets the project values" "kubernetes/vps/projects/demo/hello-kuberpack/values.yaml" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps" 2>/dev/null | jq -r '.apps[] | select(.name == "demo-hello-kuberpack") | .values_path')"
check "second app belongs to its project" "boutique" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps" 2>/dev/null | jq -r '.apps[] | select(.name == "boutique-cornichons") | .project')"
check "last build succeeded" "succeeded" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps/demo-hello-kuberpack/builds" 2>/dev/null | jq -r '.builds[0].status')"
check "image is digest pinned" "true" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps/demo-hello-kuberpack/builds" 2>/dev/null | jq -r '.builds[0].digest | test("^sha256:")')"

echo "== gitops =="
check "helmrelease ready" "True" "$(kubectl -n demo get helmrelease hello-kuberpack -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)"
check "application pod running" "true" "$(kubectl -n demo get pod -l app.kubernetes.io/name=hello-kuberpack -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null)"
check "ingress exists" "hello-kuberpack" "$(kubectl -n demo get ingress -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
check "project namespace created" "demo" "$(kubectl get namespace demo -o jsonpath='{.metadata.name}' 2>/dev/null)"

echo "== application =="
check "GET / through the ingress" "hello-kuberpack" "$(curl -sS --max-time 10 -H 'Host: hello-kuberpack.kuberpack.test' http://${K3S_NODE_IP}/ 2>/dev/null | jq -r .app)"
check "GET /healthz" "ok" "$(curl -sS --max-time 10 -H 'Host: hello-kuberpack.kuberpack.test' http://${K3S_NODE_IP}/healthz 2>/dev/null | jq -r .status)"
check "second app through the ingress" "cornichons" "$(curl -sS --max-time 10 -H 'Host: cornichons.kuberpack.test' http://${K3S_NODE_IP}/ 2>/dev/null | jq -r .app)"
check "third party application answers" "OK" "$(curl -sS --max-time 10 -H 'Host: miniflux.kuberpack.test' http://${K3S_NODE_IP}/healthcheck 2>/dev/null | head -c 2)"
check "third party application keeps its forge" "https://github.com/miniflux/v2" "$(curl -sS --max-time 10 -H "Authorization: Bearer ${FORGEJO_CONTROL_TOKEN}" "http://${K3S_NODE_IP}:30080/api/v1/apps" 2>/dev/null | jq -r '.apps[] | select(.name == "feeds-miniflux") | .repository')"

echo "== the application repository is untouched =="
# Kuberpack reads the code of the application, it never writes in it.
check "no kuberpack file in the app repo" "0" "$(curl -sS --cacert pki/ca.crt -H "Authorization: token ${FORGEJO_CONTROL_TOKEN}" --max-time 10 "https://${FORGEJO_HOST}:${FORGEJO_PORT}/api/v1/repos/kuberpack/cornichons/contents" 2>/dev/null | jq -r '[.[] | select(.name | test("kuberpack"))] | length')"

printf '\n  %d ok, %d failed\n' "${ok}" "${fail}"
[ "${fail}" -eq 0 ]

#!/usr/bin/env bash
# Follow one build Job and keep its logs plus the pod status.
#
# Usage: capture-build.sh <logfile> [seconds]
set -uo pipefail

LOGFILE="${1:-/tmp/build.log}"
DURATION="${2:-600}"
export KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/k3s-test.yaml}"

: >"${LOGFILE}"
end=$(( $(date +%s) + DURATION ))
pod=""

while [ "$(date +%s)" -lt "${end}" ]; do
    if [ -z "${pod}" ]; then
        pod=$(kubectl -n kuberpack-build get pods \
            --field-selector=status.phase=Running -o name 2>/dev/null | head -1)
        [ -n "${pod}" ] && echo "capturing ${pod}"
    fi
    if [ -n "${pod}" ]; then
        kubectl -n kuberpack-build logs "${pod}" -c builder --tail=2000 >>"${LOGFILE}" 2>/dev/null
        kubectl -n kuberpack-build get pod "${pod}" -o json 2>/dev/null \
            | jq -r '.status.containerStatuses[]? | "\(.name): exit=\(.state.terminated.exitCode // "-") reason=\(.state.terminated.reason // "-") msg=\(.state.terminated.message // "-")"' \
            >>"${LOGFILE}.status" 2>/dev/null
        phase=$(kubectl -n kuberpack-build get pod "${pod}" -o jsonpath='{.status.phase}' 2>/dev/null)
        [ "${phase}" = "Failed" ] && { echo "pod failed"; break; }
    fi
    sleep 10
done

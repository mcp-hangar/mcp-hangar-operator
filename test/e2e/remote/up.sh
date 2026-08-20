#!/usr/bin/env bash
# Boot the remote-lifecycle e2e stack (#106): kind + a live core + a minimal
# MCP backend + the released operator. The Go test in this directory then
# drives the MCPServer CR lifecycle against it.
#
# Pins live in files, not here: the core image tag in manifests/core.yaml, the
# operator tag in operator/kustomization.yaml. CORE_REF below only selects
# which tag of core's examples/task_upstream the backend image is built from --
# keep it the same release as the core image.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
CLUSTER="${E2E_REMOTE_CLUSTER:-mcp-remote-e2e}"
CORE_REF="${CORE_REF:-v2.12.0}"
BACKEND_IMAGE="localhost/mcp-task-upstream:e2e"
KCTL=(kubectl --context "kind-${CLUSTER}")

if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  # Plain kind, default CNI: unlike the reachability e2e, nothing here needs
  # NetworkPolicy enforcement. KIND_NODE_IMAGE is optional and lets CI pin the
  # same node image the crd-install job pins.
  kind create cluster --name "${CLUSTER}" ${KIND_NODE_IMAGE:+--image "${KIND_NODE_IMAGE}"}
fi

echo "waiting for the API server..."
until "${KCTL[@]}" get --raw /healthz >/dev/null 2>&1; do sleep 5; done

echo "building the MCP backend from core ${CORE_REF} (examples/task_upstream)..."
docker build -t "${BACKEND_IMAGE}" \
  "https://github.com/mcp-hangar/mcp-hangar.git#${CORE_REF}:examples/task_upstream"
kind load docker-image "${BACKEND_IMAGE}" --name "${CLUSTER}"

echo "deploying backend + core..."
"${KCTL[@]}" apply -f "${ROOT}/test/e2e/remote/manifests/backend.yaml"
"${KCTL[@]}" apply -f "${ROOT}/test/e2e/remote/manifests/core.yaml"

echo "installing CRDs (straight from config/crd/bases, like the crd-install job)..."
"${KCTL[@]}" apply --server-side -f "${ROOT}/config/crd/bases/"
# shellcheck disable=SC2046
"${KCTL[@]}" wait --for=condition=Established --timeout=60s \
  $("${KCTL[@]}" get crd -o name | grep 'mcp-hangar.io')

echo "installing the operator..."
"${KCTL[@]}" apply -k "${ROOT}/test/e2e/remote/operator"

"${KCTL[@]}" -n hangar-e2e rollout status deploy/task-upstream --timeout=180s
"${KCTL[@]}" -n hangar-e2e rollout status deploy/mcp-hangar --timeout=600s
"${KCTL[@]}" -n mcp-hangar rollout status deploy/mcp-hangar-operator-controller-manager --timeout=180s

# Warm the backend: a config-registered remote server is registered at boot but
# its tool catalogue is only discovered once the server starts. The start is
# issued from inside the core pod (the image ships python3, not curl; the API
# port is not published outside the cluster).
echo "starting the 'backend' server in core so its tools are discovered..."
POD="$("${KCTL[@]}" -n hangar-e2e get pod -l app=mcp-hangar -o jsonpath='{.items[0].metadata.name}')"
"${KCTL[@]}" -n hangar-e2e exec "${POD}" -- python3 -c '
import urllib.request
req = urllib.request.Request("http://localhost:8080/api/mcp_servers/backend/start", method="POST")
print(urllib.request.urlopen(req, timeout=60).read().decode())
'

echo "remote-lifecycle e2e stack is up (kube context kind-${CLUSTER})"

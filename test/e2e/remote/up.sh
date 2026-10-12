#!/usr/bin/env bash
# Boot the remote-lifecycle e2e stack (#106): kind + a live core + a minimal
# MCP backend + the operator built from this checkout. The Go test in this directory then
# drives the MCPServer CR lifecycle against it.
#
# Pins live in files, not here: the core image tag in manifests/core.yaml. The
# operator is built from ROOT, so the e2e exercises the code under review (#208). CORE_REF below only selects
# which tag of core's examples/task_upstream the backend image is built from --
# keep it the same release as the core image.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
CLUSTER="${E2E_REMOTE_CLUSTER:-mcp-remote-e2e}"
CORE_REF="${CORE_REF:-v2.25.0}"
BACKEND_IMAGE="localhost/mcp-task-upstream:e2e"
OPERATOR_IMAGE="localhost/mcp-hangar-operator:e2e"
KCTL=(kubectl --context "kind-${CLUSTER}")

# A local registry, the way kind documents it (#294). The operator pulls a
# mutable tag with imagePullPolicy Always, so a container-mode MCPServer cannot
# run an image that was only `kind load`ed; it has to come from a registry.
REG_NAME="kind-registry"
REG_PORT="5001"
if [ "$(docker inspect -f '{{.State.Running}}' "${REG_NAME}" 2>/dev/null || true)" != "true" ]; then
  docker run -d --restart=always -p "127.0.0.1:${REG_PORT}:5000" --network bridge --name "${REG_NAME}" registry:2
fi

if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  # Plain kind, default CNI: unlike the reachability e2e, nothing here needs
  # NetworkPolicy enforcement. KIND_NODE_IMAGE is optional and lets CI pin the
  # same node image the crd-install job pins.
  printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\ncontainerdConfigPatches:\n- |-\n  [plugins."io.containerd.grpc.v1.cri".registry]\n    config_path = "/etc/containerd/certs.d"\n' \
    | kind create cluster --name "${CLUSTER}" ${KIND_NODE_IMAGE:+--image "${KIND_NODE_IMAGE}"} --config -
fi
for node in $(kind get nodes --name "${CLUSTER}"); do
  docker exec "${node}" mkdir -p "/etc/containerd/certs.d/localhost:${REG_PORT}"
  printf '[host."http://%s:5000"]\n' "${REG_NAME}" | docker exec -i "${node}" cp /dev/stdin "/etc/containerd/certs.d/localhost:${REG_PORT}/hosts.toml"
done
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${REG_NAME}")" = "null" ]; then
  docker network connect kind "${REG_NAME}"
fi

echo "waiting for the API server..."
until "${KCTL[@]}" get --raw /healthz >/dev/null 2>&1; do sleep 5; done

echo "building the MCP backend from core ${CORE_REF} (examples/task_upstream)..."
docker build -t "${BACKEND_IMAGE}" \
  "https://github.com/mcp-hangar/mcp-hangar.git#${CORE_REF}:examples/task_upstream"
kind load docker-image "${BACKEND_IMAGE}" --name "${CLUSTER}"
# The same image for the container-mode MCPServer, pulled from the registry.
docker tag "${BACKEND_IMAGE}" "localhost:${REG_PORT}/mcp-task-upstream:e2e"
docker push "localhost:${REG_PORT}/mcp-task-upstream:e2e"

echo "building the operator from ${ROOT}..."
docker build -t "${OPERATOR_IMAGE}" "${ROOT}"
kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER}"

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

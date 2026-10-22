#!/usr/bin/env bash
# Boot the T26 stack (mcp-hangar/mcp-hangar#1950): kind with /dev/kvm in the
# node, kata-deploy with kata-qemu-coco-dev, a Trustee KBS, core behind TLS and
# the operator built from this checkout. coco_test.go then provisions the key
# and runs the kbs-attested MCPServer.
#
# Needs a host with /dev/kvm. A GitHub-hosted ubuntu-latest runner has it.
# Generated material (the KBS admin key, core's CA) goes to E2E_COCO_STATE; the
# test reads the admin key from there.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
HERE="${ROOT}/test/e2e/coco"
CLUSTER="${E2E_COCO_CLUSTER:-mcp-coco-e2e}"
STATE="${E2E_COCO_STATE:-/tmp/e2e-coco}"
KATA_VERSION="4.2.0"
OPERATOR_IMAGE="localhost/mcp-hangar-operator:e2e"
KCTL=(kubectl --context "kind-${CLUSTER}")

[ -c /dev/kvm ] || { echo "no /dev/kvm on this host" >&2; exit 1; }
mkdir -p "${STATE}"

if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  # serviceSubnet is pinned because the KBS Service's fixed ClusterIP, and so
  # the init-data hash, depend on it.
  cat <<K | kind create cluster --name "${CLUSTER}" ${KIND_NODE_IMAGE:+--image "${KIND_NODE_IMAGE}"} --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  serviceSubnet: 10.96.0.0/16
  podSubnet: 10.244.0.0/16
nodes:
  - role: control-plane
    extraMounts:
      - { hostPath: /dev/kvm, containerPath: /dev/kvm }
      - { hostPath: /dev/vhost-vsock, containerPath: /dev/vhost-vsock }
      - { hostPath: /dev/vhost-net, containerPath: /dev/vhost-net }
K
fi
until "${KCTL[@]}" get --raw /healthz >/dev/null 2>&1; do sleep 5; done

echo "installing kata-deploy ${KATA_VERSION} (kata-qemu-coco-dev only)..."
helm --kube-context "kind-${CLUSTER}" upgrade --install kata-deploy \
  oci://ghcr.io/kata-containers/kata-deploy-charts/kata-deploy --version "${KATA_VERSION}" \
  -n kube-system -f "${HERE}/kata-values.yaml" --wait --timeout 15m
until "${KCTL[@]}" get runtimeclass kata-qemu-coco-dev >/dev/null 2>&1; do sleep 5; done

echo "deploying the KBS..."
if [ ! -f "${STATE}/kbs-admin.key" ]; then
  openssl genpkey -algorithm ed25519 -out "${STATE}/kbs-admin.key"
  openssl pkey -in "${STATE}/kbs-admin.key" -pubout -out "${STATE}/kbs-admin.pub"
fi
"${KCTL[@]}" apply -f "${HERE}/manifests/kbs.yaml"
"${KCTL[@]}" -n trustee create secret generic kbs-admin-public-key \
  --from-file=public.pem="${STATE}/kbs-admin.pub" --dry-run=client -o yaml | "${KCTL[@]}" apply -f -

echo "deploying core behind TLS..."
if [ ! -f "${STATE}/core-ca.crt" ]; then
  openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 -days 2 \
    -subj /CN=e2e-coco-ca -keyout "${STATE}/core-ca.key" -out "${STATE}/core-ca.crt" 2>/dev/null
  openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 -subj /CN=mcp-hangar \
    -keyout "${STATE}/core.key" -out "${STATE}/core.csr" 2>/dev/null
  printf 'subjectAltName=DNS:mcp-hangar.hangar-e2e.svc.cluster.local,DNS:mcp-hangar.hangar-e2e.svc\n' >"${STATE}/core.ext"
  openssl x509 -req -in "${STATE}/core.csr" -CA "${STATE}/core-ca.crt" -CAkey "${STATE}/core-ca.key" \
    -CAcreateserial -days 2 -extfile "${STATE}/core.ext" -out "${STATE}/core.crt" 2>/dev/null
fi
"${KCTL[@]}" apply -f "${HERE}/manifests/core.yaml"
"${KCTL[@]}" -n hangar-e2e create secret tls core-tls --cert="${STATE}/core.crt" --key="${STATE}/core.key" \
  --dry-run=client -o yaml | "${KCTL[@]}" apply -f -

echo "building and installing the operator from ${ROOT}..."
docker build -t "${OPERATOR_IMAGE}" "${ROOT}"
kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER}"
"${KCTL[@]}" apply --server-side -f "${ROOT}/config/crd/bases/"
# shellcheck disable=SC2046
"${KCTL[@]}" wait --for=condition=Established --timeout=60s $("${KCTL[@]}" get crd -o name | grep 'mcp-hangar.io')
"${KCTL[@]}" create namespace mcp-hangar --dry-run=client -o yaml | "${KCTL[@]}" apply -f -
"${KCTL[@]}" -n mcp-hangar create secret generic core-ca --from-file=ca.crt="${STATE}/core-ca.crt" \
  --dry-run=client -o yaml | "${KCTL[@]}" apply -f -
"${KCTL[@]}" apply -k "${HERE}/operator"

"${KCTL[@]}" -n trustee rollout status deploy/kbs --timeout=300s
"${KCTL[@]}" -n hangar-e2e rollout status deploy/mcp-hangar --timeout=600s
"${KCTL[@]}" -n mcp-hangar rollout status deploy/mcp-hangar-operator-controller-manager --timeout=180s
echo "T26 stack is up (kube context kind-${CLUSTER}, state in ${STATE})"

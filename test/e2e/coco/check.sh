#!/usr/bin/env bash
# The parts of the T26 harness (mcp-hangar/mcp-hangar#1950) that need no KVM:
#
#   1. the KBS resource policy releases the key to the bound init-data hash and
#      to nothing else (opa test);
#   2. the upstream image, with a stub in place of the Confidential Data Hub,
#      serves TLS with the released key -- the SPKI a client sees is the pin
#      computed from the key -- and exits non-zero when no key is released.
#
# The stub is plain HTTP on 127.0.0.1:8006 in the container's network
# namespace, which is where the CDH answers inside a CoCo pod.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OPA_IMAGE="openpolicyagent/opa:1.20.2-static@sha256:bb245e9e36be0d0ed486c240b606c56be7aba96014a4a87895fed4ba7a6dfa8d"
PY_IMAGE="python:3.13-slim@sha256:57b29a7c050c4e679e4bea572e14b3965cf4b5839de2da49d55e49ffc4024f39"
UPSTREAM_IMAGE="localhost/coco-upstream:check"
WORK="$(mktemp -d)"
cleanup() { docker rm -f coco-cdh-stub coco-upstream >/dev/null 2>&1 || true; rm -rf "${WORK}"; }
trap cleanup EXIT

echo "== resource policy"
docker run --rm -v "${HERE}/kbs:/p:ro" "${OPA_IMAGE}" test -v /p

echo "== upstream image"
docker build -q -t "${UPSTREAM_IMAGE}" "${HERE}/upstream" >/dev/null
mkdir -p "${WORK}/cdh/resource/default/hangar-e2e"
KEY="${WORK}/cdh/resource/default/hangar-e2e/tls-key"
# named_curve: a key with explicit curve parameters gives a different SPKI than
# the certificate built from it, and the pin would never match.
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -pkeyopt ec_param_enc:named_curve -out "${KEY}" 2>/dev/null
chmod 644 "${KEY}"
WANT="$(openssl pkey -in "${KEY}" -pubout -outform DER | openssl dgst -sha256 -r | cut -c1-64)"

docker run -d --name coco-cdh-stub -v "${WORK}:/srv:ro" -w /srv "${PY_IMAGE}" \
  python -m http.server 8006 --bind 127.0.0.1 >/dev/null
docker run -d --name coco-upstream --network container:coco-cdh-stub --read-only \
  -e KBS_RESOURCE=default/hangar-e2e/tls-key -e MCP_POD_IP=127.0.0.1 "${UPSTREAM_IMAGE}" >/dev/null
GOT="$(docker run --rm --network container:coco-cdh-stub --entrypoint python "${UPSTREAM_IMAGE}" -c '
import hashlib, json, ssl, time, urllib.request
from cryptography import x509
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat
for _ in range(60):
    try:
        pem = ssl.get_server_certificate(("127.0.0.1", 8443))
        break
    except OSError:
        time.sleep(1)
else:
    raise SystemExit("the upstream never served TLS")
body = {"jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "check", "version": "0"}}}
req = urllib.request.Request("https://127.0.0.1:8443/mcp", data=json.dumps(body).encode(),
                             headers={"accept": "application/json, text/event-stream", "content-type": "application/json"})
assert urllib.request.urlopen(req, context=ssl._create_unverified_context()).status == 200
spki = x509.load_pem_x509_certificate(pem.encode()).public_key().public_bytes(Encoding.DER, PublicFormat.SubjectPublicKeyInfo)
print(hashlib.sha256(spki).hexdigest())
')"
if [ "${GOT}" != "${WANT}" ]; then
  echo "FAIL: served SPKI ${GOT}, pinned ${WANT}" >&2
  exit 1
fi
echo "served SPKI matches the pin: sha256:${WANT}"
docker rm -f coco-upstream >/dev/null

echo "== no key, no server"
if docker run --rm --network container:coco-cdh-stub --read-only -e KBS_RESOURCE=default/hangar-e2e/other-key \
  -e KEY_WAIT_S=5 -e MCP_POD_IP=127.0.0.1 "${UPSTREAM_IMAGE}"; then
  echo "FAIL: the upstream started without a released key" >&2
  exit 1
fi
echo "OK"

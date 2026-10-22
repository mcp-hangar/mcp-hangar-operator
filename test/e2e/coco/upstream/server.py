"""A key-pinned MCP upstream whose TLS key is released by a Key Broker Service (T26, mcp-hangar/mcp-hangar#1950).

The key is never in the image, a Secret or a volume. At start the server asks the
Confidential Data Hub inside the pod for KBS_RESOURCE; the CDH attests the guest
and the KBS releases the key only if its resource policy admits the evidence
(test/e2e/coco/kbs/resource-policy.rego binds it to the init-data hash).

The key stays in memory: it is written to a memfd, never to a filesystem, and the
certificate is self-signed at start -- core pins the SPKI and checks nothing else.

No key, no server: if the CDH does not release it within KEY_WAIT_S, the process
exits non-zero, so a pod the policy refuses never serves TLS.
"""

import datetime
import ipaddress
import os
import sys
import time
import urllib.error
import urllib.request

import uvicorn
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.x509.oid import NameOID
from mcp.server.mcpserver import MCPServer

CDH_URL = os.environ.get("CDH_URL", "http://127.0.0.1:8006")
RESOURCE = os.environ["KBS_RESOURCE"]  # <repo>/<type>/<tag>, the path of the key_ref
HOST = os.environ.get("MCP_POD_IP", "0.0.0.0")
PORT = int(os.environ.get("PORT", "8443"))
KEY_WAIT_S = float(os.environ.get("KEY_WAIT_S", "120"))

mcp = MCPServer("kbs-attested-upstream")


@mcp.tool(name="echo")
def echo(text: str) -> str:
    """Echo the text back."""
    return text


def fetch_key() -> bytes:
    url = f"{CDH_URL}/cdh/resource/{RESOURCE}"
    deadline = time.monotonic() + KEY_WAIT_S
    while True:
        try:
            with urllib.request.urlopen(url, timeout=30) as resp:
                return resp.read()
        except (urllib.error.URLError, OSError) as exc:
            if time.monotonic() >= deadline:
                sys.exit(f"the CDH did not release {RESOURCE}: {exc}")
            print(f"waiting for {RESOURCE}: {exc}", file=sys.stderr, flush=True)
            time.sleep(3)


def self_signed(key_pem: bytes) -> bytes:
    key = serialization.load_pem_private_key(key_pem, password=None)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "kbs-attested-upstream")])
    now = datetime.datetime.now(datetime.UTC)
    builder = (
        x509.CertificateBuilder()
        .subject_name(name)
        .issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - datetime.timedelta(minutes=5))
        .not_valid_after(now + datetime.timedelta(days=1))
    )
    if HOST != "0.0.0.0":
        builder = builder.add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address(HOST))]), False)
    return builder.sign(key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM)


def memfd(name: str, data: bytes) -> str:
    fd = os.memfd_create(name)
    os.write(fd, data)
    return f"/proc/self/fd/{fd}"


if __name__ == "__main__":
    key_pem = fetch_key()
    keyfile, certfile = memfd("key", key_pem), memfd("cert", self_signed(key_pem))
    # The SDK answers 421 to a Host it was not built for, and its default is loopback.
    app = mcp.streamable_http_app(host=HOST)
    uvicorn.run(app, host=HOST, port=PORT, ssl_certfile=certfile, ssl_keyfile=keyfile, log_level="warning")

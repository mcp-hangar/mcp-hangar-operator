"""A key-pinned MCP upstream whose TLS key is released by a Key Broker Service (T26, mcp-hangar/mcp-hangar#1950).

The standard library only, so it runs on a public Python image pinned by digest:
a CoCo guest pulls its image itself and cannot reach a registry local to kind.

The key is never in the image, a Secret or a volume. At start the server asks the
Confidential Data Hub inside the pod for KBS_RESOURCE, a PEM bundle of the key and
its certificate. The CDH attests the guest, and the KBS releases the bundle only
if its resource policy admits the evidence (kbs/resource-policy.rego binds it to
the init-data hash). The bundle stays in memory: it is written to a memfd, never
to a filesystem.

No key, no server: if the CDH does not release it within KEY_WAIT_S, the process
exits non-zero, so a pod the policy refuses never serves TLS.

It speaks just enough streamable HTTP for a client to initialize, list tools and
call `echo`, with JSON responses, on every path.
"""

import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CDH_URL = os.environ.get("CDH_URL", "http://127.0.0.1:8006")
RESOURCE = os.environ["KBS_RESOURCE"]  # <repo>/<type>/<tag>, the path of the key_ref
PORT = int(os.environ.get("PORT", "8080"))
KEY_WAIT_S = float(os.environ.get("KEY_WAIT_S", "300"))

ECHO = {
    "name": "echo",
    "description": "Echo the text back.",
    "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]},
}


def fetch_bundle() -> bytes:
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


def answer(method: str, params: dict) -> dict:
    if method == "initialize":
        return {
            "protocolVersion": params.get("protocolVersion", "2025-06-18"),
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "kbs-attested-upstream", "version": "0"},
        }
    if method == "tools/list":
        return {"tools": [ECHO]}
    if method == "tools/call" and params.get("name") == "echo":
        text = str((params.get("arguments") or {}).get("text", ""))
        return {"content": [{"type": "text", "text": text}], "isError": False}
    if method == "ping":
        return {}
    raise LookupError(method)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _send(self, status: int, body: dict | None = None) -> None:
        data = json.dumps(body).encode() if body is not None else b""
        self.send_response(status)
        if body is not None:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self._send(405)

    def do_DELETE(self):
        self._send(405)

    def do_POST(self):
        msg = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        if "id" not in msg:
            self._send(202)
            return
        try:
            self._send(200, {"jsonrpc": "2.0", "id": msg["id"], "result": answer(msg.get("method", ""), msg.get("params") or {})})
        except LookupError:
            self._send(200, {"jsonrpc": "2.0", "id": msg["id"], "error": {"code": -32601, "message": "method not found"}})


if __name__ == "__main__":
    fd = os.memfd_create("tls")
    os.write(fd, fetch_bundle())
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(f"/proc/self/fd/{fd}")
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    print(f"serving {RESOURCE} on :{PORT}", file=sys.stderr, flush=True)
    server.serve_forever()

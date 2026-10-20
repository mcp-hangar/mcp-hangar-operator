### Key-pinned remote servers need an https `hangar.url` and a core that checks keys

A remote `MCPServer` with `spec.tls.peerIdentity` is registered only when:

- the operator reaches core over `https://`, with the core's certificate verified against `hangar.tls.ca`;
- core advertises the `peer_identity` capability: a core release newer than 2.31.0, the first whose `GET /api/system/` lists `capabilities`.

Otherwise it reports `Degraded` with reason `PeerIdentityNotCarried` and is not registered. Core must also allow the endpoint under its SSRF policy, so put the network it lives on in `ssrf.trusted_networks`. The operator's core API key needs the `kubernetes-operator` role, which holds `mcp_servers:peer_identity`.

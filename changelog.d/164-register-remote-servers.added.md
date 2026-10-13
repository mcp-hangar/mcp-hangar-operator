**controller:** register a `mode: remote` MCPServer with core when core does
not hold a server by that name, and deregister it when the CR is deleted. The
CR is marked with `mcp-hangar.io/registered-by` and
`mcp-hangar.io/registered-endpoint` before core is asked. A CR naming a server
core already holds stays a mirror. A changed endpoint re-registers the server.
Core's SSRF refusal sets Degraded with reason `EndpointNotTrusted`, which names
core's `ssrf.trusted_networks`

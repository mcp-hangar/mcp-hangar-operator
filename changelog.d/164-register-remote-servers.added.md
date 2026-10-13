**controller:** register a `mode: remote` MCPServer with core when core does
not hold a server by that name, start it once before reporting Ready, and
deregister it when the CR is deleted. Which MCPServer registered a name is a
Lease `mcp-server-<name>` in the operator's namespace, claimed before core is
asked; a CR in another namespace with the same name is Degraded/`NameTaken`. A
CR naming a server core already holds stays a mirror. A changed endpoint
re-registers the server. New Degraded reasons: `EndpointNotTrusted` (names
core's `ssrf.trusted_networks`), `RegistrationForbidden`, `StartFailed`,
`RegistrationFailed`

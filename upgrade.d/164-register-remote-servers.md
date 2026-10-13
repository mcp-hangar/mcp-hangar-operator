### A remote MCPServer core does not hold is now registered with core

A `mode: remote` MCPServer whose name core does not know used to sit
Degraded with "not registered with core". The operator now adds it with
`POST /api/mcp_servers/` and removes it again when the CR is deleted.

- In-cluster endpoints need core 2.27.0 or newer with the Service range in
  `ssrf.trusted_networks`. Without it the CR is Degraded with reason
  `EndpointNotTrusted`. Every caller allowed to register servers can then reach
  that range, not only the operator; see core's upgrade note.
- With auth on, the operator's API key needs `mcp_servers:write` to register
  and `mcp_servers:lifecycle` to deregister. `provider-admin`, the role the
  docs give an operator key, has neither: the CR is then Degraded with reason
  `RegistrationForbidden`. `developer` and `admin` have both.
- A CR whose name core already holds, for instance from `config.yaml`, is
  unchanged: the operator mirrors it and never deletes it.

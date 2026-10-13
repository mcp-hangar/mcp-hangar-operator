### A remote MCPServer core does not hold is now registered with core

A `mode: remote` MCPServer whose name core did not know used to sit Degraded
with "not registered with core". The operator now adds it with
`POST /api/mcp_servers/`, starts it, and removes it again when the CR is
deleted.

- In-cluster endpoints need core 2.27.0 or newer with the Service range in
  `ssrf.trusted_networks`. Without it the CR is Degraded with reason
  `EndpointNotTrusted`. Every caller allowed to register servers can then reach
  that range, not only the operator; see core's upgrade note.
- With auth on, the operator's API key needs `mcp_servers:write` to register
  and `mcp_servers:lifecycle` to start and deregister. `provider-admin`, the
  role the docs give an operator key, has neither: the CR is then Degraded with
  reason `RegistrationForbidden`. `developer` and `admin` have both.
- The operator keeps a Lease `mcp-server-<name>` per registered server in its
  own namespace, and needs `get`, `create`, `update` and `delete` on leases
  there. `config/rbac` grants them; chart 0.12.42 does.
- `mcp-hangar.io/registered-by: operator` on a CR no longer makes the operator
  delete the server of that name in core. Anyone able to edit a CR could set
  it, in any namespace. Only the Lease does.
- A CR whose name core already holds, for instance from `config.yaml`, is
  unchanged: the operator mirrors it and never deletes it.

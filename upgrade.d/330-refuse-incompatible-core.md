### An operator against a core older than 2.27.0 now refuses it

The operator now needs core 2.27.0 or newer. Against an older core it stops calling core, where it used to set each `MCPServer` `Degraded` with its own reason, usually `EndpointNotTrusted`. Instead:

- **Event:** a Warning Event `CoreIncompatible` regarding the operator's Pod names both versions (`kubectl -n <operator namespace> get events --field-selector reason=CoreIncompatible`).
- **Metric:** `mcp_operator_core_compatibility{state="incompatible"}` is 1.
- **Status:** every `MCPServer` reports `Ready=False` with reason `CoreIncompatible`, and every `MCPEgressPolicy` reports `L7Delivered=False` with the same reason. No server is registered, no pod is started for a new container-mode server and no L7 policy is delivered until core is upgraded. Running Deployments are left as they are, and NetworkPolicies and backstops are applied as before.

Upgrade core, and the operator picks it up within about two minutes, without a restart.

The version is read from `GET /api/system/`. Since core 2.34.9 that route needs `mcp_servers:read` at global scope, which the `kubernetes-operator` role holds. A key without it now holds the operator with reason `CoreVersionUnknown`; grant the permission.

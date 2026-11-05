### Container servers are registered by the operator and need the pod network trusted in core

The operator no longer stamps the `mcp-hangar.io/enabled|name|mode|port|health-path` discovery annotations on a container-mode `MCPServer`'s pod. It registers the server with core itself at `http://<pod IP>:8080/mcp`, as it already did for key-pinned containers at `https://<pod IP>:8080`. Before upgrading:

- **Trust the pod network in core:** core refuses a private endpoint outside `ssrf.trusted_networks` (`ssrf.trustedNetworks` in the `mcp-hangar` chart). Add your cluster's pod CIDR, for example `10.244.0.0/16` on kind. Otherwise every container server goes `Degraded` with reason `EndpointNotTrusted`.
- **Let core reach the pods:** the `mcp-hangar` chart's default NetworkPolicy allows core egress only to its own namespace, DNS and port 443. For servers in other namespaces, add a `networkPolicy.additionalEgress` rule for the pod CIDR on port 8080.
- **The operator's core API key** needs `mcp_servers:write` and `mcp_servers:lifecycle`, which the `kubernetes-operator` role holds.
- **An operator without a core connection** (no `hangar.url`) registers nothing. Its container servers used to reach a core that ran kubernetes discovery; they no longer do, so point the operator at core.

What changes on upgrade:

- Every plain container server's Deployment rolls out once, since its pod template loses the annotations, so its pod restarts with a new address.
- A server core discovered from the CR's own pod is moved under the operator's Lease claim rather than registered twice, and its tools are read once more. A core without a durable configuration store cannot change the endpoint in place, so it removes the server and registers it again.
- While a pod has no address, or the server is off (`spec.replicas: 0`), the server is not in core.
- Deleting a container-mode `MCPServer` removes its server from core only when the CR holds the claim. A server core holds under that name for someone else is left alone, and the CR reports `NameTaken`.
- `spec.healthPath` is accepted and ignored. Core's discovery configuration and its pod read RBAC are no longer needed for operator servers.

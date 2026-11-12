### the operator reads each L7 policy back from every gateway pod

When `--hangar-url` is a cluster Service name
(`<service>.<namespace>.svc...`), the operator now delivers an L7 policy to
each Ready gateway pod in that namespace matched by
`--hangar-gateway-selector`, at the pod's IP, and reads it back with
`GET /api/mcp_servers/<id>/l7_policy`. Before, it pushed once through
`--hangar-url` and took a 2xx as delivered. `L7Delivered=True` now means that
every serving gateway pod answered the read-back with the policy. Pods with
the gateway label in other namespaces are not contacted. With any other
`--hangar-url` (an IP, a bare or external host name) nothing changes.

Check these before you upgrade:

- The operator's core API key needs `policy:read` as well as `policy:write`.
  The built-in `kubernetes-operator` and `provider-admin` roles hold both. A
  custom role with only `policy:write` turns every MCPEgressPolicy to
  `L7Delivered=False` (`CoreAuthRejected`) and Degraded.
- The operator must reach the gateway pods' IPs on their container port named
  `http`. A pod that names no such port is dialled on the port of
  `--hangar-url`. A NetworkPolicy that admits the operator to core's Service
  already admits it to the pods, because policy applies after the Service
  translates the address. An operator run outside the cluster (`make run`)
  usually cannot reach pod IPs and reports `L7Delivered=False` naming the pod.
- With TLS, the certificate is still verified against the `--hangar-url` host,
  or `--hangar-tls-server-name` when set, so no certificate change is needed.

The workaround of giving core's Deployment `strategy.type: Recreate` is no
longer needed for L7 re-delivery.

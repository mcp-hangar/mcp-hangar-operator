**controller:** deliver an L7 policy to each Ready gateway pod and read it
back there before reporting `L7Delivered=True`. The operator used to push
through `--hangar-url` and take a 2xx as delivered. During a rolling update of
a single-replica gateway with no durable backend (the mcp-hangar chart
default) the Service could route that push to the old pod, which was about to
be deleted. The new pod then served with no L7 policy until the informer
resync, up to about 11 hours, while the status still said delivered. When
`--hangar-url` is a cluster Service name (`<service>.<namespace>.svc...`), the
operator now dials each Ready, non-terminating pod in that namespace that
`--hangar-gateway-selector` matches. It uses the pod IP and the container port
named `http`, keeps the Service host for the Host header and TLS
verification, and reads the policy back with
`GET /api/mcp_servers/<id>/l7_policy`. Until every such pod holds the policy,
`L7Delivered` is False with a message naming the pods, the policy is
Degraded, and the push is retried every 10 seconds. Gateway-labelled pods in
other namespaces are never sent the API key. The operator's core API key now
needs `policy:read` as well as `policy:write`; the built-in
`kubernetes-operator` and `provider-admin` roles hold both. With any other
`--hangar-url`, or no Ready gateway pod in the namespace, the push goes
through `--hangar-url` as before

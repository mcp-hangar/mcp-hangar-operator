### A foreign NetworkPolicy under a server's policy name is left alone

The per-server policy is named `mcp-provider-<server>-egress`. If a
NetworkPolicy of that name exists and is not the operator's -- controlled by
something else, or with no controller and no
`app.kubernetes.io/managed-by: mcp-hangar-operator` label -- the operator no
longer rewrites its spec or deletes it. The MCPServer reports
`NetworkPolicyApplied=False/PolicyNameTaken` and a `PolicyNameTaken` Warning,
and its egress policy is not applied until the other policy is renamed or
removed. Policies the operator itself wrote, with or without an owner
reference, are managed as before.

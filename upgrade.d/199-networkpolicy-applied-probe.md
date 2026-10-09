### `NetworkPolicyApplied` is True only when something enforces the policy

`MCPServer.status.conditions[type=NetworkPolicyApplied]` used to read
`True/PolicyApplied` as soon as the per-server NetworkPolicy was written, even on
a cluster whose CNI does not enforce NetworkPolicy (kindnet, flannel, a vcluster
without policy sync). It now consults the same enforcement probe as the
MCPEgressPolicy `BackstopEnforceable` condition:

| Probe verdict | Old | New |
|---------------|-----|-----|
| enforcer observed | `True/PolicyApplied` | `True/PolicyApplied` (the message names the enforcer) |
| no enforcer observed | `True/PolicyApplied` | `False/PolicyWrittenUnenforced`, plus one `NetworkPolicyUnenforced` Warning Event on the transition |
| could not tell | `True/PolicyApplied` | `Unknown/PolicyWrittenUnverified` |

The NetworkPolicy itself is still written in every case; only the claim about it
changed. The reasons that existed before (`NoPolicyNeeded`,
`EgressWithheldUnpinnedImage`) are unchanged.

A server in the `PolicyWrittenUnenforced` or `PolicyWrittenUnverified` state is
not recorded as `capability_drift`: the policy exists, and the condition carries
the enforcement gap. A server whose policy is missing still is.

A namespace labelled `mcp-hangar.io/enforce-egress=true` now gets a
`DefaultDenyUnenforced` Warning Event when the operator creates or repairs its
default-deny egress policy and nothing in the cluster enforces it.

What to do:

- An alert or readiness gate on `NetworkPolicyApplied=True` now fires on
  clusters with no NetworkPolicy enforcement -- that is the point. Install an
  enforcing CNI, or accept the gap knowingly.
- If your CNI enforces NetworkPolicy but the probe does not recognize it, start
  the operator with `--networkpolicy-enforcement=enforced`. The same flag
  already governs the MCPEgressPolicy backstop; it now applies to all three
  writers.

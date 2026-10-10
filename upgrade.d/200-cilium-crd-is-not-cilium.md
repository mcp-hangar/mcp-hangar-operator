### The Cilium backstop flavor needs the cilium agent, not just its CRD

Upgrade the chart together with the operator. The operator now has to list
DaemonSets to recognize Cilium: the `apps/daemonsets` `get,list` grant ships in
the operator's own RBAC and in chart 0.12.19 and later. Under RBAC without it,
a Cilium cluster can no longer be confirmed, so `Auto` falls back to the
Vanilla flavor (FQDN upstreams are then denied, fail closed, and reported as
`Degraded=True/FQDNUpstreamsUnenforceable`), `BackstopEnforceable` reads
`Unknown` (unless another recognized policy API is served), and so does every
MCPServer's `NetworkPolicyApplied` (`Unknown/PolicyWrittenUnverified`).

What changes where the grant is present:

- **A working Cilium cluster:** nothing. The CRD is served and the `cilium`
  DaemonSet runs, so `Auto` keeps the Cilium flavor.
- **A cluster that serves the Cilium CRDs but runs no `cilium` agent** (left
  over after an uninstall, or installed without the agent): `Auto` switches
  from Cilium to Vanilla on the next reconcile, deletes the CiliumNetworkPolicy
  it wrote, and writes the NetworkPolicy the running CNI enforces. Hostname
  upstreams, which only the Cilium flavor can allow, become denied and are
  reported as `Degraded=True/FQDNUpstreamsUnenforceable`. They were never
  enforced there; now that is visible. `BackstopEnforceable` and
  `NetworkPolicyApplied` follow what is actually running: True for a
  recognized CNI agent or policy API, otherwise False.
- **A policy with `networkBackstop.flavor: Cilium` on such a cluster:** gets
  the Vanilla floor and `Degraded=True/CiliumAgentNotObserved`. The existing
  `CiliumUnavailable` reason still means the CRD itself is missing.
- **`--networkpolicy-enforcement=enforced|unenforced`:** still sets the
  verdict, but no longer stands in for the agent check; the flavor choice
  always comes from a look at the running DaemonSets.

Still not detected: a running cilium agent configured with
`policyEnforcementMode=never` reads as an enforcer.

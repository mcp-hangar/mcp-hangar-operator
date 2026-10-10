**controller:** a served CiliumNetworkPolicy CRD alone no longer counts as
Cilium. The CRDs outlive `cilium uninstall`, and on such a cluster the Auto
backstop flavor wrote a CiliumNetworkPolicy nobody reads, deleted the
NetworkPolicy the real CNI enforces, and reported `Enforcing`. Auto now takes
the Cilium flavor only when a `cilium` agent DaemonSet is also observed, the
enforcement probe recognizes Cilium by that agent only, and a policy that asks
for `flavor: Cilium` where no agent runs gets the Vanilla floor with
`Degraded=True/CiliumAgentNotObserved`; see UPGRADE.md

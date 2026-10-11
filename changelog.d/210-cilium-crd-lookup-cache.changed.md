**controller:** the MCPEgressPolicy controller reuses its CiliumNetworkPolicy
CRD lookup for five minutes instead of repeating it on every reconcile, which
on a cluster without Cilium was a discovery request each time

**controller:** a new MCPServer, discovery source, egress policy or group is
reconciled in the same pass that adds its finalizer, instead of returning the
deprecated `Requeue: true`; and on a cluster with Cilium the egress policy's
CiliumNetworkPolicy backstop is watched, so an edited or deleted one is put
back without waiting for the next resync

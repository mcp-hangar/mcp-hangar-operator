**controller:** the MCPServer, MCPDiscoverySource and MCPEgressPolicy
controllers write status as a merge patch against what they read, with an
optimistic lock, and skip the write when a reconcile computed the status it
already had; a steady policy no longer rewrites its status every pass

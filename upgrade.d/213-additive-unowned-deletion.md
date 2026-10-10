### Deleting an Additive, unowned discovery source keeps its servers

An `MCPDiscoverySource` with `mode: Additive` and `ownership.controller: false`
no longer deletes the MCPServers it created when the source itself is deleted;
they stay, still labelled `mcp-hangar.io/managed-by: <source>`. Delete them by
that label if you relied on the old cleanup. Sources that own their servers
(the default) or are `Authoritative` behave as before.

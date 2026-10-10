**controller:** labelling or un-labelling a namespace `mcp-hangar.io/enforce-egress`
now reconciles its MCPServers at once. An unpinned server's egress used to be
withheld (or restored) only at that server's next poll, up to ten minutes later

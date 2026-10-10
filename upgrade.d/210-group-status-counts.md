### MCPServerGroup: `initializingCount`, and no member `lastHealthCheck`

`status.coldCount` now counts only members with `spec.replicas: 0`. A member
that is starting, or that has no state yet, is counted in the new
`status.initializingCount`, so a group mid-rollout no longer reads as idle.
`status.providers[].lastHealthCheck` is removed: it made every member probe a
group status write. Read `status.lastHealthCheck` on the MCPServer instead. The
`mcp_operator_group_provider_count{state="Initializing"}` series now carries
starting members that were in `state="Cold"` before.

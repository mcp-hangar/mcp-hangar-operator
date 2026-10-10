### A rejected core API key reads `CoreAuthRejected`, not `HealthCheckFailed`

When core answers a remote MCPServer's health check with 401 or 403, its
`Degraded` condition now has reason `CoreAuthRejected`, the Warning Event is
`CoreAuthRejected` and fires once on the transition, and the server is checked
again after 5 minutes instead of 10 seconds. An alert matching
`Degraded/HealthCheckFailed` no longer fires for a wrong `--hangar-api-key`;
match `CoreAuthRejected` too. The fix is the key, not core.

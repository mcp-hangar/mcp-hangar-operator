**api:** `spec.healthPath` on MCPServer, the path core's discovery probes on a
container-mode server's pod, stamped as `mcp-hangar.io/health-path`. Unset,
core's default `/health` applies; since core 2.27.1 a 404 there falls back to an
MCP `initialize` on `/mcp`, so a server without a health endpoint is no longer
quarantined. The remote e2e now runs core's discovery with its health check on

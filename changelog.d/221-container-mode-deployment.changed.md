**controller:** container-mode MCPServers now run as a Deployment named `mcp-provider-<name>`. The operator no longer creates a fixed-name Pod. `spec.replicas` stays 0 or 1, and the strategy is `Recreate`, so two pods never serve one name to core.
- Kubelet restarts a crashed server with its own CrashLoopBackOff, in place of the operator's delete-and-recreate backoff. A crash loop or an image pull failure is reported as `Dead` with the container's waiting reason, and `status.consecutiveFailures` follows the restart count (capped at 5).
- A pod created by an earlier operator is deleted when the Deployment takes over.
- The operator needs `apps/deployments` write and no longer writes pods.

### container-mode servers run as a Deployment

Each container-mode MCPServer is now backed by a Deployment `mcp-provider-<name>`, not a Pod of that name. On the first reconcile after the upgrade, the operator creates the Deployment and deletes the old Pod. Each server restarts once.

What changes for you:

- **RBAC.** The operator's ClusterRole needs `get, list, watch, create, update, patch, delete` on `deployments` in `apps`. It no longer needs `create, update, patch` on `pods`. Upgrade the `mcp-hangar-operator` chart together with the image; a chart older than this release lacks the Deployment grant.
- **Pod names are generated** (`mcp-provider-<name>-<hash>-<id>`). `status.podName` shows the current one. Select a server's pod by label, `mcp-hangar.io/provider=<name>`, not by name.
- **Restarts are kubelet's.** A crashing server shows `CrashLoopBackOff` on its pod, and the MCPServer is `Dead` with that reason. `status.consecutiveFailures` is the pod's restart count, capped at 5. The operator's own 10-second-doubling backoff is gone.
- **Spec changes replace the pod** through the Deployment (`Recreate`). `kubectl rollout restart deploy/mcp-provider-<name>` works, and the operator keeps that annotation.
- `replicas: 0` scales the Deployment to zero; the Deployment stays.

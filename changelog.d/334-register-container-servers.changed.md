**controller:** the operator registers every container-mode MCPServer in core
at `http://<pod IP>:8080/mcp`, under the same Lease claim as a remote one, and
its pods no longer carry the discovery annotations. The endpoint follows the
pod and the server is removed from core while the pod has no address; deleting
the CR removes the server only when the CR holds the claim. A server core
discovered from the CR's own pod is moved under the claim rather than
duplicated. `spec.healthPath` is no longer used

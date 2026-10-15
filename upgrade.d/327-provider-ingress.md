### Provider ingress isolation is opt-in

Existing installs are unchanged. Configure --provider-ingress-gateway-namespace with the actual gateway namespace and --hangar-gateway-selector with its pod labels. Then set --provider-ingress-enabled=true for container MCPServers globally, or annotate selected MCPServers with mcp-hangar.io/provider-ingress: "true". An explicit "false" opts out even under the global setting and removes the owned ingress policy.

The peer combines namespace and pod selectors; matching labels in another namespace do not grant access. Only TCP/8080 (the provider discovery port) is opened. Remote targets report NotApplicable. Empty peer configuration is refused when isolation is requested. Do not widen the peer to the whole namespace to accommodate health probes; verify actual probe behavior on the CNI.

ProviderIngressApplied=True records a written policy and observed enforcement engine, not an exclusive end-to-end guarantee. Other Kubernetes NetworkPolicies may widen ingress through their additive allow rules. Verify gateway allow, non-gateway deny and actual probe behavior before adoption. Existing connections and node/host-network traffic follow CNI-specific semantics; this does not revoke established sessions or isolate a compromised node.

This release deliberately does not distinguish new objects from existing objects or enable isolation by default. A default-on migration requires a separate decision. Changing the operator image/configuration requires reconciling existing MCPServers before treating the new status as acceptance evidence.

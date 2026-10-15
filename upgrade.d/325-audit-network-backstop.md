### Audit policies stop generating a blocking network backstop

After upgrading the operator, an MCPEgressPolicy in Audit (including an omitted mode) removes its policy-owned NetworkPolicy or CiliumNetworkPolicy. Status reports BackstopApplied=False with reason AuditMode and backstopEnforcement=Disabled. L7 policy delivery continues in Audit.

Use mode: Enforce with defaultAction: Deny when the policy must restrict outbound destinations. Enforce with defaultAction: Allow now omits the policy-owned backstop (reason DefaultAllow, backstopEnforcement=Disabled); the previous allow-list backstop silently contradicted that setting. No allow-all NetworkPolicy is installed, because Kubernetes unions allow rules and it could widen other policies. Core still evaluates the configured MCP rules. Other namespace/provider policies remain independent and can still block traffic.

The new behavior does not add an ingress fence, revoke existing connections or prove that traffic cannot bypass core. Verify actual reachability on the deployment's CNI before treating a status condition as acceptance evidence.

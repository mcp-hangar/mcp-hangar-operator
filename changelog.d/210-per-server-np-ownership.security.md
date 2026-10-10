**controller:** the operator no longer overwrites or deletes a NetworkPolicy
that happens to have an MCPServer's per-server policy name but belongs to
someone else. The server reports `NetworkPolicyApplied=False/PolicyNameTaken`
with one Warning instead; a policy the operator wrote before it set owner
references is adopted. See UPGRADE.md

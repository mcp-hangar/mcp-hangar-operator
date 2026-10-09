**controller:** an MCPServer no longer reports `NetworkPolicyApplied=True` for a
per-server NetworkPolicy that nothing in the cluster enforces. The condition now
consults the same enforcement probe as the MCPEgressPolicy backstop:
`True/PolicyApplied` only when an enforcer is observed,
`False/PolicyWrittenUnenforced` (plus one `NetworkPolicyUnenforced` Warning) when
none is, and `Unknown/PolicyWrittenUnverified` when the probe cannot tell. An
enforce-egress namespace gets a `DefaultDenyUnenforced` Warning when its
default-deny is written where nothing enforces it. Policies are still written in
every case; see UPGRADE.md

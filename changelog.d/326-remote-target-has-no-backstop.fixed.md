**controller:** an MCPEgressPolicy whose target is a `mode: remote` MCPServer, or a
group of only remote servers, no longer reports `BackstopApplied=True` and
`backstopEnforcement: Enforcing`. A remote server has no pods, so the
NetworkPolicy built from its selector selected nothing. No backstop is written (and
one left behind is removed); the policy reports `BackstopApplied=False` with
reason `RemoteTargetHasNoPods` and `backstopEnforcement: NotApplicable`, and the L7
rules are still delivered to core

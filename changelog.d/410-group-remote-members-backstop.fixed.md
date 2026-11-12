**controller:** an `MCPEgressPolicy` on an `MCPServerGroup` with both
`mode: container` and `mode: remote` members no longer reports
`backstopEnforcement: Enforcing` for the whole group. The backstop selects the
pods of the container members only; a remote member's traffic leaves from the
gateway pod, so only the L7 rules in core apply to it. Such a policy now keeps
its backstop, reports `BackstopApplied=True` with reason
`RemoteMembersNotCovered` and a message naming the remote members, and reads
`backstopEnforcement: Partial` where it used to read `Enforcing`. `Unenforced`
and `Unverified` are reported as before, and `Degraded` is not set. Groups of
only container members, and groups of only remote members (`NotApplicable`),
report as before

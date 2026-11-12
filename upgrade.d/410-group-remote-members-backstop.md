### A policy on a group with remote members reports `Partial`, not `Enforcing`

An `MCPEgressPolicy` that targets an `MCPServerGroup` with at least one
`mode: container` member and at least one `mode: remote` member used to report
`BackstopApplied=True` and `status.backstopEnforcement: Enforcing`, and nothing
in its status named the remote members. The backstop never covered them: their
traffic leaves from the gateway pod. Such a policy now reports
`backstopEnforcement: Partial`, and `BackstopApplied=True` with reason
`RemoteMembersNotCovered` and a message that names the remote members.

- Nothing is enforced less than before. The container members keep their
  backstop, and the L7 rules in core still reach every member.
- The CRD's `backstopEnforcement` enum gains `Partial`; apply the new CRD before
  or with the new operator image. With the old CRD the API server rejects the
  status write of every such policy.
- An alert, dashboard or script that treats anything but `Enforcing` as a
  problem will see these policies now. `Partial` does not set `Degraded`; to
  cover a remote member at the network level, restrict the gateway pod's own
  egress.
- Groups of only container members still report `Enforcing`, and groups of only
  remote members still report `NotApplicable`.

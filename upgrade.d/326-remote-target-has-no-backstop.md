### A policy on a remote server reports `NotApplicable`, not `Enforcing`

An `MCPEgressPolicy` that targets a `mode: remote` MCPServer used to report
`BackstopApplied=True` and `status.backstopEnforcement: Enforcing`, over a
NetworkPolicy that selected no pod. It now reports `BackstopApplied=False`
(reason `RemoteTargetHasNoPods`) and `backstopEnforcement: NotApplicable`, and
writes no NetworkPolicy.

- Nothing was enforcing at the network level before either. The L7 rules in core
  are the only enforcement a remote target has, and they are still delivered.
- A group whose members are all remote reports the same. A group with at least
  one container-mode member still gets its backstop.
- The CRD's `backstopEnforcement` enum gains `NotApplicable`; apply the new CRD.
  An alert or dashboard that treats anything but `Enforcing` as a problem will see
  these policies now.

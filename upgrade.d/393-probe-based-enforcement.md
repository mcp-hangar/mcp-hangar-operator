### Backstop enforcement is reported from an observed block

`BackstopEnforcement`, `BackstopEnforceable` and an MCPServer's `NetworkPolicyApplied` now rest on the operator's reachability probe, not on recognizing the CNI. With `--networkpolicy-enforcement=auto` (the default) the operator creates two Deployments in its own namespace, `mcp-hangar-enforcement-probe-fenced` and `mcp-hangar-enforcement-probe-target`, and a NetworkPolicy `mcp-hangar-enforcement-probe`. They run the operator's own image with no service account token, request 5m CPU and 16Mi memory each, and are owned by the operator's pod, so they go with it.

- **kindnet and other CNIs the operator did not recognize** move from `Unenforced` with `Degraded=True` to `Enforcing`. If you set `operator.networkPolicyEnforcement: enforced` only to silence that, you can go back to `auto`.
- **Calico, Cilium and the other recognized CNIs** read `Unverified` for the first minute after the operator starts, then `Enforcing` once the block is observed. They stay `Unverified` if the canaries cannot run or the operator cannot reach the fenced one on port 6443, for example under a namespace policy that denies ingress to all pods; allow that port from the operator to pods labelled `mcp-hangar.io/enforcement-probe`.
- **A cluster where the probe connects through its own policy** reads `Unenforced`, whatever CNI it names.
- **To run no canaries**, set `--networkpolicy-enforcement` to `enforced` or `unenforced`; the status then states the asserted answer as before.

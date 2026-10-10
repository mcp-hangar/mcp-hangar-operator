### `spec.replicas` accepts only 0 or 1, and `kubectl scale` is refused

An MCPServer runs one pod. `spec.replicas` used to accept 0 to 10 and the CRD
served a scale subresource, so a higher value or `kubectl scale` was accepted
and silently ran one pod anyway. Now:

- `spec.replicas` is `0` (off, no pod) or `1` (on, the default). A create or
  update that sets it above 1 is refused by the apiserver.
- `kubectl scale mcpserver ...` and anything else using the scale subresource
  (an HPA, a KEDA ScaledObject) get `NotFound`. Remove such autoscalers: they
  never changed the number of pods.
- `status.replicas` reports the pods that exist, 0 or 1; it was never written
  before.

A stored object with `replicas` above 1 keeps working. On Kubernetes 1.30 and
later CRD validation ratcheting (beta and on by default in 1.30, GA in 1.33) lets updates through as long as they leave that
field unchanged; set it to `1` the next time you edit the object.

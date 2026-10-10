**api:** `MCPServer.spec.replicas` is an on/off switch (0 or 1), and the
scale subresource is gone. The CRD accepted up to 10 and served
`kubectl scale`, but the operator always ran one pod, so `replicas: 3` and
`kubectl scale --replicas=5` were accepted and ignored. `status.replicas` now
reports how many pods exist; see UPGRADE.md

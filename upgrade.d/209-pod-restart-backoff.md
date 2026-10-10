### Failing provider pods are retried, with a backoff that holds

A container-mode MCPServer whose pod fails is restarted after 10 s, 20 s,
40 s and 80 s, then every 10 minutes while it keeps failing. Before, the pod
came back at once after each failure (the backoff never applied) and, after
five failures, was never restarted until someone edited the server. A spec
change resets the count. `status.consecutiveFailures` stops at 5.

A pod that exits with status 0 now leaves the server `Initializing` with
condition `Progressing/PodExited` and is restarted; it used to read `Cold`,
which otherwise means `replicas: 0`.

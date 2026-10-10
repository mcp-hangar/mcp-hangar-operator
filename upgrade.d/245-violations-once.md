### `capability_violations_total` counts violations, not reconciles

`mcp_operator_capability_violations_total`, the `status.violations` history and
the `ViolationDetected` Warning now count a violation when it starts, and again
only if it clears and returns. An alert on the counter's rate fires less often
for the same ongoing problem; alert on the `ViolationDetected` condition, whose
message now reads `Active violations: <type>, ...`, to catch one that persists.

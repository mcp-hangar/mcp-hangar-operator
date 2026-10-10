**controller:** a capability violation that persists is recorded once. A
NetworkPolicy that stays unapplied or a tool count that stays over its limit
used to add a `status.violations` entry, increment
`mcp_operator_capability_violations_total` and emit a Warning on every
reconcile. A violation is recorded when it starts and again only if it clears
and returns; `ViolationDetected` lists the active types. See UPGRADE.md

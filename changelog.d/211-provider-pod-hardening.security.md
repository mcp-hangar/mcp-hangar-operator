**controller:** provider pods keep their restricted security defaults when
the MCPServer sets only part of `podSecurityContext` or
`containerSecurityContext` (a partial value used to replace them whole, and
adding a capability no longer re-grants the dropped ones); an unset
`spec.resources` gets small requests instead of a BestEffort pod; a
tag-referenced image is pulled every time; and a deleted server's
`capability_violations_total` series are removed. See UPGRADE.md

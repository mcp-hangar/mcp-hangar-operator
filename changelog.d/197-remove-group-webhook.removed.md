**webhook:** the MCPServerGroup validating webhook is gone. Its one rule,
`spec.selector` must be set, is the CRD schema's `required`, so a group without
a selector is still rejected, now without a `failurePolicy: Fail` webhook hop

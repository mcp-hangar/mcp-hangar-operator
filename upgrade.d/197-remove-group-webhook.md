### The MCPServerGroup webhook is removed

The operator no longer serves `/validate-mcp-hangar-io-v1alpha2-mcpservergroup`.
A `ValidatingWebhookConfiguration` that still routes MCPServerGroup writes there
makes every group create and update fail, because its `failurePolicy` is
`Fail`. Apply the new `config/webhook` manifests, or a chart that no longer
renders `vmcpservergroup-v1alpha2.kb.io`, with or before this operator image.
A group without `spec.selector` is still rejected, by the CRD schema.

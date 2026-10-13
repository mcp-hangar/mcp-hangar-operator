### The webhook no longer enforces the wildcard egress and image digest rules

The ValidatingAdmissionPolicies in `config/admissionpolicy` (chart
`admissionPolicies.enabled`, on by default) are now the only thing that refuses
wildcard egress without `hangar.io/allow-unrestricted-egress: "true"` and that
warns on or blocks an image not pinned by digest.

- With the policies left out -- `admissionPolicies.enabled=false` in the chart,
  or `config/admissionpolicy` dropped from a kustomize overlay -- neither rule
  exists, whether or not the webhooks run.
- Without the params ConfigMap the digest policies allow everything. Set the
  level as `imageDigestPolicy` in it (`warn`, `block`, `off`); the chart sets it
  from `operator.imageDigestPolicy`.
- `--webhook-annotation-rules` and `--image-digest-policy` have no effect. The
  operator still accepts them and logs that it ignores them; a later release
  removes them, after which a manifest that passes them fails to start. Drop
  them from your manifests now.

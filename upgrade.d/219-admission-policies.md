### The wildcard egress and image digest rules are admission policies

`config/default` (and `install.yaml`) now installs three
ValidatingAdmissionPolicies with their bindings and a params ConfigMap,
`mcp-hangar-operator-admission-policy-params`, and starts the operator with
`--webhook-annotation-rules=false`. The rules hold whether or not the webhooks
run, which was not true before: with webhooks off, nothing refused wildcard
egress without `hangar.io/allow-unrestricted-egress: "true"`.

- If you set `--image-digest-policy`, set the same value as `imageDigestPolicy`
  in the params ConfigMap; the flag only reaches the webhook copy. Without the
  ConfigMap the digest policies allow everything.
- Updates are ratcheted: an existing MCPServer that breaks a rule can still be
  labelled, reconciled and deleted; changing its image, egress or the opt-in
  annotation is judged again.
- `--webhook-annotation-rules` and the webhook copies of the two rules are
  removed in the next release. Install the policies before then.

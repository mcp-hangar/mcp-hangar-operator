### Spec fields that never did anything are removed from v1alpha2

These fields were accepted, validated and stored, and no controller acted on them. They are gone from the `v1alpha2` schema, which stays the only served version:

| Kind | Removed field | What it claimed | What happened |
| --- | --- | --- | --- |
| `MCPServer` | `spec.startupTimeout` | a startup deadline | nothing |
| `MCPServer` | `spec.healthPath` | the path core's discovery probed | already ignored since the last release, which registers container-mode servers itself |
| `MCPServer` | `spec.capabilities.enforcementMode` | `block` or `quarantine` a violating server | copied into each violation record's `action`; nothing was blocked or quarantined |
| `MCPServer` | `spec.capabilities.tools.expectedTools` | a `schema_mismatch` violation for an unexpected tool | nothing |
| `MCPServer` | `spec.capabilities.tools.schemaDriftAlert` | an alert on schema change | nothing |
| `MCPServerGroup` | `spec.healthPolicy.unhealthyThreshold` | consecutive failures before unhealthy | nothing |
| `MCPDiscoverySource` | `spec.ownership.blockDeletion` | hold deletion until servers are gone | nothing |
| `MCPEgressPolicy` | `spec.upstreams[].match.toolSchemaDigestRef`, `.imageDigest`, `.issuers` | per-upstream trust conditions | dropped; only `match.host` reaches the backstop |

The `MCPServer` fields go from `MCPDiscoverySource` `spec.providerTemplate.spec` too, which embeds `MCPServerSpec`, and `enforcementMode` from `MCPServer` `status.capabilities`, which mirrors the spec. `status.violations[].type` now accepts only `capability_drift` and `undeclared_tool`, the two the operator ever wrote; `egress_denied`, `schema_mismatch` and `quarantine_triggered` were never emitted. `status.violations[].action` is always `alert` from now on; records an older operator stored with `block` or `quarantine` still validate.

**A manifest that sets a removed field is rejected, not silently changed.** `kubectl apply`, `create` and `replace` use strict field validation by default, so the API server refuses the field as unknown (`unknown field "spec.startupTimeout"`). A client that sends no `fieldValidation` gets a warning instead, and the API server prunes the field before storing the object. Delete the fields from your manifests, Helm values and GitOps sources before applying them again.

Objects already stored need nothing. Their stored values, including the API-server defaults several of these fields carried (`enforcementMode: alert`, `schemaDriftAlert: true`, `unhealthyThreshold: 3`, `blockDeletion: false`), are pruned on the next write of the object. The order of applying the CRDs and upgrading the operator does not matter: the operator never read the fields.

The schema is now guarded by a test that fails when a spec field has no reader in the operator, so a declaration that does nothing cannot land again.

### Provider pods: merged security context, default requests, `Always` pull for tags

- `spec.podSecurityContext` and `spec.containerSecurityContext` are now laid
  over the restricted defaults field by field instead of replacing them. A
  server that set only `fsGroup` used to lose `runAsNonRoot` and the seccomp
  profile; it now keeps them. To turn a default off, set that field
  explicitly (e.g. `readOnlyRootFilesystem: false`). A `capabilities` block
  without its own `drop` list keeps `drop: [ALL]`.
- With no `spec.resources`, the container requests `50m` CPU and `64Mi`
  memory, so the pod is Burstable rather than BestEffort. No limits are set.
  Namespaces with a ResourceQuota on requests now count these.
- An image referenced by tag (no `@sha256:`) gets `imagePullPolicy: Always`;
  a digest-pinned image keeps `IfNotPresent`. A tag-referenced server whose
  node cannot reach the registry no longer starts from a cached image; pin it
  by digest.

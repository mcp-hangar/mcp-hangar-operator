# Upgrade notes

## Unreleased — `MCPServer` pod fields are the `corev1` types

`MCPServerSpec` used to re-declare Kubernetes pod primitives as hand-rolled
subsets. They are now the `k8s.io/api/core/v1` types verbatim, in `v1alpha2`
itself — no new API version, no conversion webhook. `v1alpha2` is alpha and
`v1alpha1` was retired one release ago; adding a `v1alpha3` would only repeat
that cycle.

Anything a `PodSpec` accepts is now accepted here: projected/CSI/downwardAPI
volumes, `env.valueFrom.fieldRef` and `resourceFieldRef`, extended resources,
`seLinuxOptions`, `sysctls`, `privileged`, `procMount`, and so on. Previously
those had no field to reject — they were silently inexpressible.

**Applying an old manifest unchanged is an error, not a silent drop:** the
removed spellings are unknown fields, and `kubectl apply` rejects them under
its default strict field validation. Edit the manifest first.

| Old | New | What to change |
| --- | --- | --- |
| `spec.resources.requests.cpu` / `.memory` (string) | `spec.resources.requests.<resource>` (`resource.Quantity`) | Nothing for `"500m"` / `"1Gi"` — quantities parse the same strings. The `requests`/`limits` maps are now open: any resource name works, not just `cpu` and `memory`. |
| `spec.env[]` (custom `EnvVar`) | `spec.env[]` (`corev1.EnvVar`) | `valueFrom.secretKeyRef` / `configMapKeyRef` are unchanged (`name`, `key`, `optional`). |
| `spec.volumes[]` (volume **and** its mount in one object) | `spec.volumes[]` (`corev1.Volume`) + `spec.volumeMounts[]` (`corev1.VolumeMount`) | Split each entry in two. `mountPath`, `subPath` and `readOnly` move to a `spec.volumeMounts` entry with the same `name`; the source moves under the volume's inline `VolumeSource` (`secret.secretName`, `configMap.name`, `persistentVolumeClaim.claimName`, `emptyDir`). |
| `spec.securityContext` (one struct fed to **both** pod and container) | `spec.podSecurityContext` (`corev1.PodSecurityContext`) + `spec.containerSecurityContext` (`corev1.SecurityContext`) | `spec.securityContext` no longer exists. Pod-level keys (`runAsNonRoot`, `runAsUser`, `runAsGroup`, `fsGroup`, `seccompProfile`) go to `podSecurityContext`; container-level keys (`runAsNonRoot`, `runAsUser`, `runAsGroup`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation`, `capabilities`, `seccompProfile`) go to `containerSecurityContext`. Keys that used to be set on both need to be written twice — that duplication was hidden before, and is what made the field lossy in the other direction. |
| `spec.tolerations[]` (custom `Toleration`) | `spec.tolerations[]` (`corev1.Toleration`) | No change: identical field names and values. |

Unchanged: leaving a security context unset still gets the operator's
restricted defaults, per context. Setting one still replaces that context
wholesale rather than merging into the defaults.

Example, before:

```yaml
spec:
  volumes:
    - name: config
      mountPath: /config
      readOnly: true
      configMap:
        name: provider-config
  securityContext:
    fsGroup: 2000
    readOnlyRootFilesystem: true
```

after:

```yaml
spec:
  volumes:
    - name: config
      configMap:
        name: provider-config
  volumeMounts:
    - name: config
      mountPath: /config
      readOnly: true
  podSecurityContext:
    fsGroup: 2000
  containerSecurityContext:
    readOnlyRootFilesystem: true
```

`MCPDiscoverySource.spec.providerTemplate.spec` is an `MCPServerSpec`, so the
same edits apply there.

The CRDs grow as a result (the `corev1` volume sources carry their own
schemas): `mcpservers` ~93 KB → ~253 KB, `mcpdiscoverysources` ~106 KB →
~286 KB. **Install them with `kubectl apply --server-side`.** Client-side apply
stores the whole manifest in the `last-applied-configuration` annotation, and
annotations are capped at 256 KB — `mcpdiscoverysources` no longer fits.
Helm is unaffected (it does not use that annotation).

## 0.16.0 — v1alpha1 is no longer served

`mcp-hangar.io/v1alpha1` (`MCPServer`, `MCPServerGroup`, `MCPDiscoverySource`)
is unserved as of this release: `kubectl apply`/`get` of a v1alpha1 manifest is
rejected by the apiserver. Objects created as v1alpha1 are unaffected — storage
has been v1alpha2 since 0.15.x, so they stay readable and writable as
`mcp-hangar.io/v1alpha2`. `MCPEgressPolicy` was v1alpha2-only from the start.

Migration is a one-line change per manifest: `apiVersion: mcp-hangar.io/v1alpha2`.
Field-level differences were already handled by conversion (durations such as
`startupTimeout`/`shutdownGracePeriod`/`refreshInterval` are typed durations
like `30s`, not free-form strings).

The compatibility window ran from v0.15.3 (first release whose controllers
speak v1alpha2, 2026-08-17) to this release. Rollback, if something in your
cluster still applies v1alpha1: pin the operator image and chart back to
0.15.3 — v1alpha1 is served there.

The v1alpha1 Go types, validators and the conversion webhook are deleted in
this same release (the deletion PR merged ahead of the release cut); nothing
user-visible changes beyond the unserve itself. The wildcard-egress opt-in
guard moved to the v1alpha2 validator on the way -- it had lived only in the
v1alpha1 one.

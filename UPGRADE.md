# Upgrade notes

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

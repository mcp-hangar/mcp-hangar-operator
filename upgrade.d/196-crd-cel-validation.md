### MCPServer mode and MCPEgressPolicy targetRef are immutable, and the CRDs validate without the webhook

The MCPServer, MCPDiscoverySource and MCPEgressPolicy CRDs now carry CEL and
schema rules for what the validating webhooks used to check. The webhooks are
off by default (`--enable-webhooks=false`, chart `webhook.enabled: false`), so
until now a cluster on the defaults stored all of the following; it now refuses
them on create and update, whether or not the webhook runs:

- `MCPServer` with `mode: container` and no `image`, or `mode: remote` and no
  `endpoint`, or an endpoint that is not an absolute `http`/`https` URL with a
  host (`javascript:alert(1)`, `/only/path`).
- A negative or unparseable `startupTimeout` / `shutdownGracePeriod`
  (`-5s`, `banana`).
- `capabilities.tools.expectedTools` with an empty or a duplicate entry, more
  than 256 entries, or an entry longer than 256 characters.
- An egress rule whose `cidr` is not an IPv4 CIDR such as `10.0.0.0/8` or an
  IPv6 CIDR such as `fd00::/8` (`10.0.0.0/33`, a bare address).
- `image` longer than 1024 characters, `endpoint` longer than 2048.
- `MCPDiscoverySource` with `type: ConfigMap` and no `configMapRef`.

Two updates that used to be accepted are now refused by the apiserver:

- Changing `MCPServer.spec.mode` (`container` to `remote` or back). Delete the
  MCPServer and create it again in the new mode. An `MCPDiscoverySource` whose
  entry for an existing server switches mode records the refused update in
  `status.discoveredProviders[].error` on every sync until you delete that
  MCPServer; the next sync recreates it in the new mode.
- Changing `MCPEgressPolicy.spec.targetRef`. Delete the policy and create one
  for the new target.

Objects stored before the upgrade that break a new rule stay readable and are
not rewritten. Kubernetes 1.30+ ratchets CRD validation, so an update that
leaves the offending field unchanged (labels, finalizers, status) still goes
through; an update that touches it must make it valid. Find them before
upgrading with, for example,
`kubectl get mcpservers -A -o json | jq -r '.items[] | select(.spec.mode == "container" and ((.spec.image // "") == "")) | .metadata.namespace + "/" + .metadata.name'`.

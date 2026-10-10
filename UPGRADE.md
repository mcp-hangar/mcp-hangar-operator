# Upgrade notes

## Upgrade to 0.17.11

### A rejected core API key reads `CoreAuthRejected`, not `HealthCheckFailed`

When core answers a remote MCPServer's health check with 401 or 403, its
`Degraded` condition now has reason `CoreAuthRejected`, the Warning Event is
`CoreAuthRejected` and fires once on the transition, and the server is checked
again after 5 minutes instead of 10 seconds. An alert matching
`Degraded/HealthCheckFailed` no longer fires for a wrong `--hangar-api-key`;
match `CoreAuthRejected` too. The fix is the key, not core.

### `cidr: 0.0.0.0/0` and `::/0` need the unrestricted-egress annotation

With the validating webhook enabled, an MCPServer whose egress rule has a
zero-length CIDR (`0.0.0.0/0`, `::/0`) is refused on create and update unless
it carries `hangar.io/allow-unrestricted-egress: "true"`, the opt-in that
`host: "*"` already required. These rules open every destination, which the
old gate did not see. Add the annotation to keep such a server as it is, or
narrow the CIDR. With the annotation, the operator emits a Warning Event
`UnrestrictedEgressAllowed`, as it does for `host: "*"`.

## Upgrade to 0.17.10

### `spec.replicas` accepts only 0 or 1, and `kubectl scale` is refused

An MCPServer runs one pod. `spec.replicas` used to accept 0 to 10 and the CRD
served a scale subresource, so a higher value or `kubectl scale` was accepted
and silently ran one pod anyway. Now:

- `spec.replicas` is `0` (off, no pod) or `1` (on, the default). A create or
  update that sets it above 1 is refused by the apiserver.
- `kubectl scale mcpserver ...` and anything else using the scale subresource
  (an HPA, a KEDA ScaledObject) get `NotFound`. Remove such autoscalers: they
  never changed the number of pods.
- `status.replicas` reports the pods that exist, 0 or 1; it was never written
  before.

A stored object with `replicas` above 1 keeps working. On Kubernetes 1.30 and
later CRD validation ratcheting (beta and on by default in 1.30, GA in 1.33) lets updates through as long as they leave that
field unchanged; set it to `1` the next time you edit the object.

### `/metrics` needs HTTPS and an authorized bearer token

The operator's metrics endpoint (`--metrics-bind-address`, `:8080` by default)
now serves HTTPS with a self-signed certificate generated in memory, and
refuses a request without a bearer token the API server accepts and authorizes
for `get` on the `/metrics` non-resource URL. A scraper still using plain HTTP,
or no token, gets nothing; the metrics are not lost, only unread until the
scrape config changes.

To keep scraping:

- Scrape `https`, skip certificate verification (or pin the certificate), and
  send the scraper's ServiceAccount token. With the Prometheus Operator:
  `scheme: https`, `bearerTokenFile:
  /var/run/secrets/kubernetes.io/serviceaccount/token` (or `authorization`),
  `tlsConfig.insecureSkipVerify: true`.
- Bind the scraper's ServiceAccount to a ClusterRole allowing `get` on the
  `/metrics` non-resource URL. `config/rbac/metrics_reader_role.yaml` ships one
  named `metrics-reader`.
- The operator itself now needs `create` on `tokenreviews` and
  `subjectaccessreviews`; `config/rbac/role.yaml` carries it. A chart that does
  not grant it leaves every scrape refused.

To keep the old behaviour, start the operator with `--metrics-secure=false`.

## Upgrade to 0.17.8

### Core calls give up after 5 s, and four reconciles run at once

A call from the operator to core (`--hangar-url`) now has 5 s in all, retries
included; it used to allow 10 s per attempt and up to four attempts. A core
that takes longer than 5 s to answer a health, tools or L7-policy request now
fails that call: the MCPServer or MCPEgressPolicy reports it the way it reports
an unreachable core and retries on its next requeue.

The MCPServer and MCPEgressPolicy controllers now run 4 reconciles at once
instead of 1, set with `--max-concurrent-reconciles` (at least 1). Pass
`--max-concurrent-reconciles=1` to keep the old behaviour.

## Upgrade to 0.17.7

### The Cilium backstop flavor needs the cilium agent, not just its CRD

Upgrade the chart together with the operator. The operator now has to list
DaemonSets to recognize Cilium: the `apps/daemonsets` `get,list` grant ships in
the operator's own RBAC and in chart 0.12.19 and later. Under RBAC without it,
a Cilium cluster can no longer be confirmed, so `Auto` falls back to the
Vanilla flavor (FQDN upstreams are then denied, fail closed, and reported as
`Degraded=True/FQDNUpstreamsUnenforceable`), `BackstopEnforceable` reads
`Unknown` (unless another recognized policy API is served), and so does every
MCPServer's `NetworkPolicyApplied` (`Unknown/PolicyWrittenUnverified`).

What changes where the grant is present:

- **A working Cilium cluster:** nothing. The CRD is served and the `cilium`
  DaemonSet runs, so `Auto` keeps the Cilium flavor.
- **A cluster that serves the Cilium CRDs but runs no `cilium` agent** (left
  over after an uninstall, or installed without the agent): `Auto` switches
  from Cilium to Vanilla on the next reconcile, deletes the CiliumNetworkPolicy
  it wrote, and writes the NetworkPolicy the running CNI enforces. Hostname
  upstreams, which only the Cilium flavor can allow, become denied and are
  reported as `Degraded=True/FQDNUpstreamsUnenforceable`. They were never
  enforced there; now that is visible. `BackstopEnforceable` and
  `NetworkPolicyApplied` follow what is actually running: True for a
  recognized CNI agent or policy API, otherwise False.
- **A policy with `networkBackstop.flavor: Cilium` on such a cluster:** gets
  the Vanilla floor and `Degraded=True/CiliumAgentNotObserved`. The existing
  `CiliumUnavailable` reason still means the CRD itself is missing.
- **`--networkpolicy-enforcement=enforced|unenforced`:** still sets the
  verdict, but no longer stands in for the agent check; the flavor choice
  always comes from a look at the running DaemonSets.

Still not detected: a running cilium agent configured with
`policyEnforcementMode=never` reads as an enforcer.

## Upgrade to 0.17.6

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
not rewritten. Kubernetes 1.30+ ratchets CRD validation (the
`CRDValidationRatcheting` feature gate, on by default from 1.30): a rule is
re-checked only when the value it is attached to changes. For the per-field
rules (durations, `cidr`, `expectedTools`, the length limits) that means an
update that leaves the offending field alone still goes through. The `image`
and `endpoint` rules are attached to the whole `spec`, so a stored container
server with no image, or a remote one with a bad endpoint, refuses *any* spec
change -- `replicas`, `kubectl scale`, a discovery re-sync -- until the
same update fixes it; metadata and status updates still go through. A stored
bad `cidr` or `expectedTools` entry can also block the controller's status
write when it copies the capabilities into a `status.capabilities` that did not
already hold them. Find such objects
before upgrading, for example
`kubectl get mcpservers -A -o json | jq -r '.items[] | select(.spec.mode == "container" and ((.spec.image // "") == "")) | .metadata.namespace + "/" + .metadata.name'`.

### `NetworkPolicyApplied` is True only when something enforces the policy

`MCPServer.status.conditions[type=NetworkPolicyApplied]` used to read
`True/PolicyApplied` as soon as the per-server NetworkPolicy was written, even on
a cluster whose CNI does not enforce NetworkPolicy (kindnet, flannel, a vcluster
without policy sync). It now consults the same enforcement probe as the
MCPEgressPolicy `BackstopEnforceable` condition:

| Probe verdict | Old | New |
|---------------|-----|-----|
| enforcer observed | `True/PolicyApplied` | `True/PolicyApplied` (the message names the enforcer) |
| no enforcer observed | `True/PolicyApplied` | `False/PolicyWrittenUnenforced`, plus one `NetworkPolicyUnenforced` Warning Event on the transition |
| could not tell | `True/PolicyApplied` | `Unknown/PolicyWrittenUnverified` |

The NetworkPolicy itself is still written in every case; only the claim about it
changed. The reasons that existed before (`NoPolicyNeeded`,
`EgressWithheldUnpinnedImage`) are unchanged.

A server in the `PolicyWrittenUnenforced` or `PolicyWrittenUnverified` state is
not recorded as `capability_drift`: the policy exists, and the condition carries
the enforcement gap. A server whose policy is missing still is.

A namespace labelled `mcp-hangar.io/enforce-egress=true` now gets a
`DefaultDenyUnenforced` Warning Event when the operator creates or repairs its
default-deny egress policy and nothing in the cluster enforces it.

What to do:

- An alert or readiness gate on `NetworkPolicyApplied=True` now fires on
  clusters with no NetworkPolicy enforcement -- that is the point. Install an
  enforcing CNI, or accept the gap knowingly.
- If your CNI enforces NetworkPolicy but the probe does not recognize it, start
  the operator with `--networkpolicy-enforcement=enforced`. The same flag
  already governs the MCPEgressPolicy backstop; it now applies to all three
  writers.

## Upgrade to 0.17.5

### a ConfigMap discovery source can no longer read another namespace

An `MCPDiscoverySource` of type `ConfigMap` used to read
`spec.configMapRef.namespace` from any namespace, with the operator's
cluster-wide ConfigMap access, and create the `MCPServer`s its entries describe
in the source's own namespace. Since `mode: container` entries keep their image,
command and args, that let whoever could write a ConfigMap in one namespace,
together with whoever could create a source in another, start containers in the
second namespace without permission to create pods there.

`spec.configMapRef.namespace` must now be empty or the source's own namespace.

- With the admission webhook on, creating such a source, or changing its
  reference to another namespace, is rejected. An existing source whose
  reference is unchanged can still be updated -- labels, annotations, the
  finalizer -- with an admission warning, so it can be deleted.
- With the webhook off (the chart default), the controller reads nothing and
  creates nothing. It reports `Synced=False` and `Ready=False` with reason
  `CrossNamespaceRefused`, a message naming both namespaces, and one Warning
  Event with the same reason.

`MCPServer`s such a source created before the upgrade are left running: the
refusal deletes nothing, even for an `Authoritative` source. Find the sources
before upgrading:

```sh
kubectl get mcpdiscoverysources -A -o json | jq -r '.items[]
  | select(.spec.type == "ConfigMap")
  | select(.spec.configMapRef.namespace // "" | . != "")
  | select(.spec.configMapRef.namespace != .metadata.namespace)
  | "\(.metadata.namespace)/\(.metadata.name) -> \(.spec.configMapRef.namespace)"'
```

Copy the ConfigMap into the source's namespace and drop
`configMapRef.namespace`, or delete the source, which removes the servers it
created.

### the namespace default-deny backstop is owned by its Namespace and a foreign one is refused

The `mcp-default-deny-egress` NetworkPolicy the operator writes into a
namespace labelled `mcp-hangar.io/enforce-egress=true` used to carry no owner
and was not watched: deleting or editing it left the namespace fail-open until
the Namespace object changed or the next resync, hours later. It is now
controlled by the Namespace (an owner reference) and watched, so a deleted
policy is recreated and an edited one restored within seconds, and it is
garbage-collected with the namespace.

Policies written by an earlier operator carry no owner but do carry
`app.kubernetes.io/managed-by: mcp-hangar-operator`; the upgrade adopts those
in place. A policy under that name that is neither -- owned by another
controller, or written by hand without the label -- used to be silently
overwritten. It is now left alone: the operator emits a Warning Event
`DefaultDenyNotOwned` on the Namespace, re-checks every five minutes, and
applies its backstop only once that policy is gone. If you see the Event,
remove or rename the foreign policy; until then the namespace has whatever
egress that policy allows, not the operator's DNS-only default.

### provider pods no longer mount a ServiceAccount token

A container-mode `MCPServer` pod used to get a ServiceAccount token projected
into it, the way any pod does when nothing says otherwise: with
`serviceAccountName` empty that was the namespace default ServiceAccount, so
every MCP server had a bearer token for the API server on disk whether or not
it ever used one. A NetworkPolicy does not block the API server on many CNIs,
so an egress policy did not close that path.

The operator now writes `automountServiceAccountToken: false` on every
provider pod it builds unless the `MCPServer` opts in:

```yaml
spec:
  serviceAccountName: my-server
  automountServiceAccountToken: true
```

If a server reads `/var/run/secrets/kubernetes.io/serviceaccount` -- it talks
to the Kubernetes API, or uses an in-cluster client library that does -- it
will start failing with "unable to load in-cluster configuration" or a `401`
after the upgrade. Add `automountServiceAccountToken: true` to that server,
preferably together with a dedicated `serviceAccountName` carrying only the
RBAC it needs. A server that never touched the token sees no change beyond the
missing mount; nothing is rewritten on existing pods until their next
generation rolls them.

### an `MCPEgressPolicy` with `networkBackstop.generate: false` now delivers its L7 rules

An `MCPEgressPolicy` that opted out of the L3/L4 backstop with
`spec.networkBackstop.generate: false` used to lose its tool, argument and
header rules with it: the reconcile stopped at the backstop decision and never
pushed the compiled L7 policy to core. The policy read `Compiled=True` and
`BackstopApplied=False` / `BackstopGenerationDisabled`, which says only that the
backstop is off; nothing said that core held no rules for its servers either.

The two layers are now independent. Such a policy still writes no backstop and
still reports `BackstopApplied=False` / `BackstopGenerationDisabled`, and its L7
policy is pushed to core for every target server, reported on `L7Delivered`
like any other policy's (`True` / `Delivered`, or `False` with the push
failure's reason and `Degraded=True` / `L7PushFailed`). The finalizer clears it
from core on delete, as it already did.

What changes in the cluster is enforcement: a `generate: false` policy in
`Enforce` mode whose tool rules were silently dropped until now is enforced by
core after the upgrade. If such a policy was written with a narrow `allow`
list, or a `defaultAction: Deny`, on the assumption that only its backstop
mattered, tool calls it never blocked before will be blocked, or routed to
approval, on the next reconcile. Review those policies before upgrading, or set
them to `Audit` mode first and read the decisions core logs.

A `generate: false` policy whose target does not exist now reports
`Compiled=False` / `TargetNotFound` and `Degraded=True`, and is re-checked
every 30 seconds, as a `generate: true` policy with a missing target always
did. It used to read `Compiled=True` with nothing to compile for.

### the pod-registration webhook now gates pod UPDATE, and the provider label is immutable

In an `mcp-hangar.io/enforce-egress=true` namespace the pod-registration
webhook used to be called on pod CREATE only. A pod admitted without the
`mcp-hangar.io/provider` label could then be labelled with a registered
server's name -- `kubectl label pod <pod> mcp-hangar.io/provider=<name>` --
with no admission call at all, and because the per-server allow
`NetworkPolicy` selects pods by exactly that label, the pod inherited that
server's egress. The README's "shadow provider pods fail to deploy" held only
at creation.

The webhook is now called on UPDATE as well, and once a pod is admitted its
provider label is immutable:

| Write on an admitted pod in an enforced namespace | Before | Now |
| --- | --- | --- |
| Add `mcp-hangar.io/provider=<name>` (registered or not) | Allowed, no admission call | Denied |
| Change `mcp-hangar.io/provider` from one name to another (both registered or not) | Allowed, no admission call | Denied |
| Remove `mcp-hangar.io/provider` | Allowed | Allowed -- the pod leaves the server's allow-policy |
| Any update that leaves the label as it was (finalizers, ownerRefs, annotations, other labels) | Allowed | Allowed, without a lookup -- a pod whose `MCPServer` was deleted can still be cleaned up |

Pod CREATE is unchanged: a labelled pod is admitted when the named `MCPServer`
exists in the namespace. The operator's own pods carry the label from creation
and the controller never relabels a pod, so none of its writes are affected.
Kubelet status writes go through the `pods/status` subresource, which the rule
(`resources: pods`) does not match, so they never reach the webhook.

If a workflow of yours relied on labelling a running pod into a server, create
the pod with the label instead. The Helm chart's
`ValidatingWebhookConfiguration` must list `UPDATE` alongside `CREATE` for the
`vpod-registration.kb.io` rule for this to take effect; the chart release that
pairs with this operator version does.

### a ConfigMap discovery entry in `mode: container` now keeps its image

An `MCPDiscoverySource` of type `ConfigMap` used to drop `image`, `command` and
`args` from every entry: a `mode: container` entry became an `MCPServer` with
no image, which the server controller marked `Dead` with `InvalidSpec`
("Container mode requires image"), and the source reported `Synced=True` as if
nothing was wrong.

It now carries the three fields into the `MCPServer` spec. The
`providerTemplate` is the default and an entry that sets a field wins, which is
what already happened to `endpoint`. Existing managed servers pick the fields up
on the next sync, so a container entry that has been `Dead` since it was
created starts for the first time after the upgrade -- check that is what you
want before upgrading an operator that manages such a ConfigMap.

A container entry that names no image, and whose source has no
`providerTemplate.spec.image` to fall back on, no longer becomes a `Dead`
server at all. The source skips it, lists it in
`status.discoveredProviders` with `managed: false` and the reason in `error`,
and reports `Synced=False` with reason `PartialFailure`. A server such an entry
created before the upgrade is left alone; delete it or give the entry an image.

### an `MCPEgressPolicy` now says whether core took its L7 policy

An `MCPEgressPolicy` whose compiled L7 policy core refused -- a 403 from an API
key without `policy:write`, a core that was down, a payload core rejected --
used to keep reporting `Compiled=True`, `BackstopApplied=True` and
`Degraded=False`, with a Warning Event as the only trace. The network half was
enforced; the tool, argument and header rules were never anywhere.

It now carries an `L7Delivered` condition, also shown as the `L7` column of
`kubectl get mcpegresspolicies`:

- `True` / `Delivered` once every target server accepted the push.
- `True` / `DeliveredNotPersisted` when core took it but has no persistence
  backend, so the policy is gone after a gateway restart. The operator re-pushes
  it when a gateway pod becomes Ready; this reason only tells you the gap exists.
- `False` / `CoreAuthRejected`, `CoreUnreachable` or `PushFailed`, naming the
  server whose push failed. The policy is also `Degraded=True` with reason
  `L7PushFailed`.
- `Unknown` / `CoreIntegrationOff` when the operator runs without
  `--hangar-url` and pushes nothing.

`Compiled` and `BackstopApplied` mean what they did. What changes is that a
policy core has been rejecting since it was created now says so -- so an alert
on `Degraded` may fire on policies that read green until now. That is the
finding, not a regression: fix the key's permissions (or core's reachability)
and the next reconcile clears it.

## Upgrade to 0.17.3

### an `MCPEgressPolicy` can now report `Degraded` where it used to report success

An `MCPEgressPolicy` whose backstop the operator wrote used to report
`Degraded=False` whether or not anything in the cluster enforced it. It now
looks for a policy-enforcing API or a known CNI agent in the API server it
writes to, and when it finds neither it reports
`status.backstopEnforcement: Unenforced`, a `BackstopEnforceable=False`
condition, a `BackstopUnenforced` Event, and `Degraded=True` with reason
`EnforcementNotObserved`.

Nothing changes about what is written, and the L7 rules core enforces are
untouched. What changes is that a cluster where the backstop was never enforced
now says so -- so an alert on `Degraded` may fire on policies that have been
inert since they were created. That is the finding, not a regression: check
whether those pods actually have the egress the policy denies before silencing
it.

If your CNI enforces `NetworkPolicy` but is not recognized (the operator knows
Cilium, Calico, Canal, Antrea, AWS VPC CNI, Kube-OVN, OVN-Kubernetes,
kube-router, Weave Net and Azure NPM), assert it with the new flag rather than
living with the warning:

```bash
--networkpolicy-enforcement=enforced
```

and please open an issue naming the CNI, so the next person does not need the
flag.

## Upgrade to 0.17.0

### `MCPServer` pod fields are the `corev1` types

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

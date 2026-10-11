# Changelog

## [0.17.18](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.17...v0.17.18) (2026-10-11)

### Added

- **api:** `kubectl get --field-selector` works on MCPServer `spec.mode` and
  `status.state`, and on MCPEgressPolicy `spec.mode` and `spec.targetRef.kind`
  (Kubernetes 1.31 or later) ([#287](https://github.com/mcp-hangar/mcp-hangar-operator/pull/287))
- **infra:** the operator logs its version and commit at startup, stamped into
  the binary by the image build, which is now `-trimpath` with stripped symbols
  and digest-pinned base images ([#281](https://github.com/mcp-hangar/mcp-hangar-operator/pull/281))

### Changed

- **controller:** an MCPServerGroup counts starting members, and members with no
  state yet, in the new `status.initializingCount` instead of `coldCount`, and
  its member list no longer carries `lastHealthCheck`, so a member's health
  probe no longer rewrites the group status ([#289](https://github.com/mcp-hangar/mcp-hangar-operator/pull/289))
- **controller:** an MCPServerGroup no longer takes a finalizer, so deleting one
  no longer waits for a running operator; its metrics are cleared when the
  operator sees it gone, and the finalizer is removed from existing groups ([#290](https://github.com/mcp-hangar/mcp-hangar-operator/pull/290))
- **controller:** the MCPEgressPolicy controller reuses its CiliumNetworkPolicy
  CRD lookup for five minutes instead of repeating it on every reconcile, which
  on a cluster without Cilium was a discovery request each time ([#291](https://github.com/mcp-hangar/mcp-hangar-operator/pull/291))

### Removed

- **api:** `MCPServer.status.phase` is gone. Nothing ever wrote it; `status.state`
  and the conditions carry the server's state ([#284](https://github.com/mcp-hangar/mcp-hangar-operator/pull/284))
- **webhook:** the MCPServerGroup validating webhook is gone. Its one rule,
  `spec.selector` must be set, is the CRD schema's `required`, so a group without
  a selector is still rejected, now without a `failurePolicy: Fail` webhook hop ([#286](https://github.com/mcp-hangar/mcp-hangar-operator/pull/286))

### Fixed

- **controller:** a cold MCPServer's `Ready` condition no longer says it "will
  start on demand"; nothing scales `spec.replicas` up from 0, and the message now
  says to set it to 1. The `startupTimeout` field doc says the operator does not
  act on it ([#285](https://github.com/mcp-hangar/mcp-hangar-operator/pull/285))

## [0.17.17](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.16...v0.17.17) (2026-10-10)

### Changed

- **webhook:** the admission warning for a host-only egress rule now says to
  add a `cidr` or allow the hostname with an MCPEgressPolicy using the Cilium
  flavor, instead of pointing at a Tetragon backend that does not exist ([#275](https://github.com/mcp-hangar/mcp-hangar-operator/pull/275))

### Fixed

- **controller:** a capability violation that persists is recorded once. A
  NetworkPolicy that stays unapplied or a tool count that stays over its limit
  used to add a `status.violations` entry, increment
  `mcp_operator_capability_violations_total` and emit a Warning on every
  reconcile. A violation is recorded when it starts and again only if it clears
  and returns; `ViolationDetected` lists the active types. See UPGRADE.md ([#280](https://github.com/mcp-hangar/mcp-hangar-operator/pull/280))
- **api:** `MCPDiscoverySource.spec.refreshInterval` must be a non-negative
  duration. The schema accepted any string, and a value such as `banana` was
  stored and then broke the operator's decoding of that source ([#278](https://github.com/mcp-hangar/mcp-hangar-operator/pull/278))
- **infra:** the pod-registration webhook reads the MCPServer straight from the
  API server, so a pod applied right after its server is no longer denied
  because the cache had not caught up; with webhooks on, a replica is ready only
  once its webhook server listens; and the kustomize Deployment's
  `terminationGracePeriodSeconds` is 15 s, longer than the 10 s drain, so the
  leader lease release is not cut off ([#276](https://github.com/mcp-hangar/mcp-hangar-operator/pull/276))

## [0.17.16](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.15...v0.17.16) (2026-10-10)

### Fixed

- **controller:** a new MCPServer, discovery source, egress policy or group is
  reconciled in the same pass that adds its finalizer, instead of returning the
  deprecated `Requeue: true`; and on a cluster with Cilium the egress policy's
  CiliumNetworkPolicy backstop is watched, so an edited or deleted one is put
  back without waiting for the next resync ([#273](https://github.com/mcp-hangar/mcp-hangar-operator/pull/273))

## [0.17.15](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.14...v0.17.15) (2026-10-10)

### Changed

- **controller:** Events fire when something changes, not on every poll. An
  unhealthy or unreachable remote MCPServer warned every 10 s, a healthy one
  logged "is ready"/"is healthy" every 5 minutes, and every discovery refresh
  emitted `SyncStarted` and `SyncCompleted`. These now fire on the condition
  transition (and `SyncCompleted` when the sync outcome changes); `SyncStarted`
  is gone ([#271](https://github.com/mcp-hangar/mcp-hangar-operator/pull/271))

## [0.17.14](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.13...v0.17.14) (2026-10-10)

### Fixed

- **controller:** discovery refuses a generated MCPServer name Kubernetes cannot
  use (uppercase, `/`, longer than 63 characters) per entry, with the reason in
  `status.discoveredProviders`, instead of failing at Create; and deleting an
  `Additive` source with `ownership.controller: false` leaves its servers in
  place instead of deleting them. See UPGRADE.md ([#268](https://github.com/mcp-hangar/mcp-hangar-operator/pull/268))

### Security

- **controller:** the operator no longer overwrites or deletes a NetworkPolicy
  that happens to have an MCPServer's per-server policy name but belongs to
  someone else. The server reports `NetworkPolicyApplied=False/PolicyNameTaken`
  with one Warning instead; a policy the operator wrote before it set owner
  references is adopted. See UPGRADE.md ([#269](https://github.com/mcp-hangar/mcp-hangar-operator/pull/269))

## [0.17.13](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.12...v0.17.13) (2026-10-10)

### Security

- **controller:** provider pods keep their restricted security defaults when
  the MCPServer sets only part of `podSecurityContext` or
  `containerSecurityContext` (a partial value used to replace them whole, and
  adding a capability no longer re-grants the dropped ones); an unset
  `spec.resources` gets small requests instead of a BestEffort pod; a
  tag-referenced image is pulled every time; and a deleted server's
  `capability_violations_total` series are removed. See UPGRADE.md ([#266](https://github.com/mcp-hangar/mcp-hangar-operator/pull/266))

## [0.17.12](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.11...v0.17.12) (2026-10-10)

### Fixed

- **controller:** a failing provider pod is restarted with a real backoff (10 s
  doubling to 5 min, then every 10 min once it has failed five times in a row)
  instead of at once and then never; the failure count is capped and reset by a
  spec change; and a pod that exits 0 is restarted with the server
  `Initializing`, not reported `Cold` ([#264](https://github.com/mcp-hangar/mcp-hangar-operator/pull/264))

## [0.17.11](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.10...v0.17.11) (2026-10-10)

### Fixed

- **controller:** a remote MCPServer whose core rejects the operator's
  credentials (401/403) reads `Degraded/CoreAuthRejected` with one Warning,
  re-checked at the steady interval, instead of `HealthCheckFailed` every 10 s
  with a Warning each time, indistinguishable from an outage.
  `--hangar-ca-file` and `--hangar-tls-server-name` let the operator reach an
  https core whose certificate a private CA signed ([#262](https://github.com/mcp-hangar/mcp-hangar-operator/pull/262))

### Security

- **webhook:** an egress rule with `cidr: 0.0.0.0/0` or `::/0` now needs the
  `hangar.io/allow-unrestricted-egress: "true"` annotation, and its use is
  audited with `UnrestrictedEgressAllowed`, as `host: "*"` already was. The CIDR
  forms opened every destination with neither; see UPGRADE.md ([#261](https://github.com/mcp-hangar/mcp-hangar-operator/pull/261))

## [0.17.10](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.9...v0.17.10) (2026-10-10)

### Changed

- **api:** `MCPServer.spec.replicas` is an on/off switch (0 or 1), and the
  scale subresource is gone. The CRD accepted up to 10 and served
  `kubectl scale`, but the operator always ran one pod, so `replicas: 3` and
  `kubectl scale --replicas=5` were accepted and ignored. `status.replicas` now
  reports how many pods exist; see UPGRADE.md ([#259](https://github.com/mcp-hangar/mcp-hangar-operator/pull/259))

### Security

- **infra:** `/metrics` is served over HTTPS and requires a bearer token the API
  server authenticates and authorizes for `get` on the `/metrics` non-resource
  URL. Served plain, any pod in the cluster could read server names, states, tool
  counts and reconcile errors. `--metrics-secure=false` restores plain HTTP; see
  UPGRADE.md ([#258](https://github.com/mcp-hangar/mcp-hangar-operator/pull/258))

## [0.17.9](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.8...v0.17.9) (2026-10-10)

### Added

- **controller:** `--dns-egress-selectors` names resolver pods other than
  `kube-system/k8s-app=kube-dns` that DNS may reach, such as
  `openshift-dns/dns.operator.openshift.io/daemonset-dns=default` on OpenShift,
  in the per-server policy, the namespace default-deny and both backstop
  flavors. Clusters whose resolver is not kube-dns lost DNS in governed
  namespaces, and `--dns-egress-cidrs` could not reach a Service ClusterIP ([#255](https://github.com/mcp-hangar/mcp-hangar-operator/pull/255))

### Fixed

- **controller:** the operator's memory no longer grows with every ConfigMap and
  Service in the cluster. Discovery reads them straight from the API server
  instead of through cluster-wide informers, and cached objects (Pods included)
  are kept without their `managedFields` ([#256](https://github.com/mcp-hangar/mcp-hangar-operator/pull/256))

### Security

- **config:** the operator's ClusterRole no longer grants cluster-wide read on
  `secrets` and `serviceaccounts`, which nothing in the operator reads, and the
  leader-election Role no longer grants full CRUD on `configmaps`: the lock is a
  Lease ([#254](https://github.com/mcp-hangar/mcp-hangar-operator/pull/254))

## [0.17.8](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.7...v0.17.8) (2026-10-10)

### Fixed

- **controller:** one slow or unreachable core no longer stalls every MCPServer.
  A core call now gives up after 5 s with its retries included (it used to hold
  a reconcile for about 43 s on a core that accepted connections and never
  answered), and the MCPServer and MCPEgressPolicy controllers run 4 reconciles
  at once (`--max-concurrent-reconciles`), so a container server's pod is created
  while remote servers wait on core ([#250](https://github.com/mcp-hangar/mcp-hangar-operator/pull/250))

## [0.17.7](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.6...v0.17.7) (2026-10-10)

### Fixed

- **controller:** labelling or un-labelling a namespace `mcp-hangar.io/enforce-egress`
  now reconciles its MCPServers at once. An unpinned server's egress used to be
  withheld (or restored) only at that server's next poll, up to ten minutes later ([#247](https://github.com/mcp-hangar/mcp-hangar-operator/pull/247))

### Security

- **controller:** a served CiliumNetworkPolicy CRD alone no longer counts as
  Cilium. The CRDs outlive `cilium uninstall`, and on such a cluster the Auto
  backstop flavor wrote a CiliumNetworkPolicy nobody reads, deleted the
  NetworkPolicy the real CNI enforces, and reported `Enforcing`. Auto now takes
  the Cilium flavor only when a `cilium` agent DaemonSet is also observed, the
  enforcement probe recognizes Cilium by that agent only, and a policy that asks
  for `flavor: Cilium` where no agent runs gets the Vanilla floor with
  `Degraded=True/CiliumAgentNotObserved`; see UPGRADE.md ([#249](https://github.com/mcp-hangar/mcp-hangar-operator/pull/249))

## [0.17.6](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.5...v0.17.6) (2026-10-09)

### Changed

- **api:** the apiserver now enforces the MCPServer, MCPDiscoverySource and
  MCPEgressPolicy rules that used to live only in the validating webhooks, which
  are off by default: a container-mode MCPServer needs an `image`, a remote-mode
  one an absolute `http`/`https` `endpoint` with a host, `startupTimeout` and
  `shutdownGracePeriod` must be non-negative durations, `expectedTools` entries
  must be non-empty and unique, an egress `cidr` must be a well-formed CIDR, and a
  `ConfigMap` discovery source needs a `configMapRef`. `MCPServer.spec.mode` and
  `MCPEgressPolicy.spec.targetRef` are now immutable. The webhook keeps only the
  checks the schema cannot express (annotation opt-ins, the cross-namespace
  ConfigMap reference, filter regexps) and its warnings; see UPGRADE.md ([#240](https://github.com/mcp-hangar/mcp-hangar-operator/pull/240))

### Security

- **controller:** an MCPServer no longer reports `NetworkPolicyApplied=True` for a
  per-server NetworkPolicy that nothing in the cluster enforces. The condition now
  consults the same enforcement probe as the MCPEgressPolicy backstop:
  `True/PolicyApplied` only when an enforcer is observed,
  `False/PolicyWrittenUnenforced` (plus one `NetworkPolicyUnenforced` Warning) when
  none is, and `Unknown/PolicyWrittenUnverified` when the probe cannot tell. An
  enforce-egress namespace gets a `DefaultDenyUnenforced` Warning when its
  default-deny is written where nothing enforces it. Policies are still written in
  every case; see UPGRADE.md ([#239](https://github.com/mcp-hangar/mcp-hangar-operator/pull/239))

## [0.17.5](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.4...v0.17.5) (2026-10-09)


### Fixed

* **controller:** carry image, command and args from ConfigMap discovery entries ([#233](https://github.com/mcp-hangar/mcp-hangar-operator/issues/233)) ([eb6f131](https://github.com/mcp-hangar/mcp-hangar-operator/commit/eb6f1319337d23d1d1cfbc469dbe77dfd8191dc5)), closes [#206](https://github.com/mcp-hangar/mcp-hangar-operator/issues/206)
* **controller:** delete only the operator's own default-deny when a namespace opts out ([#237](https://github.com/mcp-hangar/mcp-hangar-operator/issues/237)) ([12c7693](https://github.com/mcp-hangar/mcp-hangar-operator/commit/12c76936df9883f123a4686bf2f81d3262b9ce64)), closes [#236](https://github.com/mcp-hangar/mcp-hangar-operator/issues/236)
* **controller:** deliver the L7 rules of an MCPEgressPolicy with networkBackstop.generate=false ([#229](https://github.com/mcp-hangar/mcp-hangar-operator/issues/229)) ([c86dc50](https://github.com/mcp-hangar/mcp-hangar-operator/commit/c86dc500f40d822c4292356dc6fb3c8ef6cf68d5))
* **controller:** follow membership changes of a policy's target ([#226](https://github.com/mcp-hangar/mcp-hangar-operator/issues/226)) ([f4f01c1](https://github.com/mcp-hangar/mcp-hangar-operator/commit/f4f01c1e7f1ef05f0feaf2a4c7bb2fee5d854438)), closes [#190](https://github.com/mcp-hangar/mcp-hangar-operator/issues/190)
* **controller:** own and watch the namespace default-deny backstop ([#232](https://github.com/mcp-hangar/mcp-hangar-operator/issues/232)) ([e83dca6](https://github.com/mcp-hangar/mcp-hangar-operator/commit/e83dca638937e92c8af0ec6e85890f24245b3dca)), closes [#204](https://github.com/mcp-hangar/mcp-hangar-operator/issues/204)
* **controller:** provider pods mount a ServiceAccount token by default ([#231](https://github.com/mcp-hangar/mcp-hangar-operator/issues/231)) ([8c6af36](https://github.com/mcp-hangar/mcp-hangar-operator/commit/8c6af36dcb9bbdce9b8415f24cdecb66e1986d43)), closes [#207](https://github.com/mcp-hangar/mcp-hangar-operator/issues/207)
* **controller:** refuse a ConfigMap discovery source that references another namespace ([#235](https://github.com/mcp-hangar/mcp-hangar-operator/issues/235)) ([de0ee99](https://github.com/mcp-hangar/mcp-hangar-operator/commit/de0ee9930ecef17ab2d131c1103598cdacc512b4))
* **controller:** report a failed L7 push on MCPEgressPolicy as L7Delivered=False and Degraded ([#227](https://github.com/mcp-hangar/mcp-hangar-operator/issues/227)) ([b8cf708](https://github.com/mcp-hangar/mcp-hangar-operator/commit/b8cf7089a826a6dcf67e9bbb4fc7b1ba21ac1751))
* **webhook:** gate pod UPDATE so an admitted pod cannot be relabelled into a registered server's egress ([#225](https://github.com/mcp-hangar/mcp-hangar-operator/issues/225)) ([c72bca5](https://github.com/mcp-hangar/mcp-hangar-operator/commit/c72bca51bda15692d95f924deb285e5e60451d81)), closes [#189](https://github.com/mcp-hangar/mcp-hangar-operator/issues/189)

## [0.17.4](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.3...v0.17.4) (2026-09-24)


### Fixed

* **controller:** re-deliver L7 policies when a gateway pod becomes Ready ([#183](https://github.com/mcp-hangar/mcp-hangar-operator/issues/183)) ([804b861](https://github.com/mcp-hangar/mcp-hangar-operator/commit/804b861ac3f6babb59511a6f95ef59fdc9a0cc7f))

## [0.17.3](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.2...v0.17.3) (2026-09-21)


### Fixed

* **controller:** report whether a written backstop has anything to enforce it ([#177](https://github.com/mcp-hangar/mcp-hangar-operator/issues/177)) ([2414d42](https://github.com/mcp-hangar/mcp-hangar-operator/commit/2414d42ef3d6d94817c9f9848281af3baec7d580)), closes [#172](https://github.com/mcp-hangar/mcp-hangar-operator/issues/172)

## [0.17.2](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.1...v0.17.2) (2026-09-17)


### Fixed

* **controller:** give the ready path's Degraded condition a reason ([#175](https://github.com/mcp-hangar/mcp-hangar-operator/issues/175)) ([a483a06](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a483a06b843e6c0e2305eb1f64ba06fd5fcd5c06)), closes [#174](https://github.com/mcp-hangar/mcp-hangar-operator/issues/174)

## [0.17.1](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.17.0...v0.17.1) (2026-08-24)


### Added

* **api:** an egress policy can select on Mcp-Param-* headers ([#161](https://github.com/mcp-hangar/mcp-hangar-operator/issues/161)) ([5010359](https://github.com/mcp-hangar/mcp-hangar-operator/commit/5010359d19d6b5797dad67d64dc27644650ad653)), closes [#160](https://github.com/mcp-hangar/mcp-hangar-operator/issues/160)

## [0.17.0](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.16.0...v0.17.0) (2026-08-20)


### ⚠ BREAKING CHANGES

* **api:** MCPServer spec.resources takes resource.Quantity values in an open requests/limits map; spec.volumes is corev1.Volume and its mount moves to the new spec.volumeMounts; spec.securityContext is replaced by spec.podSecurityContext and spec.containerSecurityContext. See UPGRADE.md.

### Added

* **api:** embed the corev1 pod types on MCPServer instead of a lossy subset ([#153](https://github.com/mcp-hangar/mcp-hangar-operator/issues/153)) ([edcf782](https://github.com/mcp-hangar/mcp-hangar-operator/commit/edcf7827a54e3d0c695db551d9fe94c7605f1f22))
* **controller:** emit Events through events.k8s.io/v1, and turn SA1019 back on ([#150](https://github.com/mcp-hangar/mcp-hangar-operator/issues/150)) ([ca19ef8](https://github.com/mcp-hangar/mcp-hangar-operator/commit/ca19ef815585e87b55a26befadcfc19f41715f67)), closes [#58](https://github.com/mcp-hangar/mcp-hangar-operator/issues/58)
* **webhook:** warn when a CIDR egress rule cannot match in-cluster on Cilium ([#154](https://github.com/mcp-hangar/mcp-hangar-operator/issues/154)) ([63ca2d3](https://github.com/mcp-hangar/mcp-hangar-operator/commit/63ca2d38c075c8c6027c14b09a4344cae188e5d2)), closes [#152](https://github.com/mcp-hangar/mcp-hangar-operator/issues/152)


### Fixed

* **ci:** make lint runs the linter CI runs ([#155](https://github.com/mcp-hangar/mcp-hangar-operator/issues/155)) ([a4b388f](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a4b388faec3fd711d8db90bab9e72e88d03e1738))

## [0.16.0](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.15.3...v0.16.0) (2026-08-17)


### ⚠ BREAKING CHANGES

* **api:** mcp-hangar.io/v1alpha1 manifests are no longer accepted; apply them as mcp-hangar.io/v1alpha2.

### Added

* **api:** stop serving v1alpha1 ([#136](https://github.com/mcp-hangar/mcp-hangar-operator/issues/136)) ([89c3a82](https://github.com/mcp-hangar/mcp-hangar-operator/commit/89c3a82db938cf7c32048ea4ad15c6278798de7e))

### Fixed

* **api:** delete the unserved v1alpha1 API, and port the wildcard-egress guard it was hiding ([#137](https://github.com/mcp-hangar/mcp-hangar-operator/issues/137)) ([f5c2041](https://github.com/mcp-hangar/mcp-hangar-operator/commit/f5c2041)) -- merged ahead of the release cut, so it ships in 0.16.0, not a follow-up


### Fixed

* **api:** delete the unserved v1alpha1 API, and port the wildcard-egress guard it was hiding ([#137](https://github.com/mcp-hangar/mcp-hangar-operator/issues/137)) ([f5c2041](https://github.com/mcp-hangar/mcp-hangar-operator/commit/f5c2041057064d60ab86ce0151fb28e45bc6ca7c))


### Changed

* **ci:** pre-1.0 breaking changes bump minor, and this release is 0.16.0 ([#139](https://github.com/mcp-hangar/mcp-hangar-operator/issues/139)) ([0562641](https://github.com/mcp-hangar/mcp-hangar-operator/commit/0562641faaadc6bff1cf1fa4c92574494b1e7e88))

## [0.15.3](https://github.com/mcp-hangar/mcp-hangar-operator/compare/v0.15.2...v0.15.3) (2026-08-17)


### Fixed

* **api:** remove MCPServer idleTTL, healthCheck, and circuitBreaker ([#127](https://github.com/mcp-hangar/mcp-hangar-operator/issues/127)) ([317983e](https://github.com/mcp-hangar/mcp-hangar-operator/commit/317983e477e11e509af85f7e654b211293ef40f6)), closes [#120](https://github.com/mcp-hangar/mcp-hangar-operator/issues/120)
* **api:** remove MCPServer observability and unused capability declarations ([#130](https://github.com/mcp-hangar/mcp-hangar-operator/issues/130)) ([3b22f06](https://github.com/mcp-hangar/mcp-hangar-operator/commit/3b22f065191d5eaa90de63e26b7277b3e07fff00)), closes [#121](https://github.com/mcp-hangar/mcp-hangar-operator/issues/121)
* **api:** remove MCPServer spec.tools, which nothing enforced ([#126](https://github.com/mcp-hangar/mcp-hangar-operator/issues/126)) ([1600f8b](https://github.com/mcp-hangar/mcp-hangar-operator/commit/1600f8b4fe312279ac06b2677355f928ac4a35df)), closes [#119](https://github.com/mcp-hangar/mcp-hangar-operator/issues/119)
* **api:** remove MCPServerGroup failover, sessionAffinity, and circuitBreaker ([#129](https://github.com/mcp-hangar/mcp-hangar-operator/issues/129)) ([854a09a](https://github.com/mcp-hangar/mcp-hangar-operator/commit/854a09a93baf6ae06aef62d31f28b818736c444a)), closes [#123](https://github.com/mcp-hangar/mcp-hangar-operator/issues/123)
* **api:** remove MCPServerGroup spec.strategy and status.activeStrategy ([#128](https://github.com/mcp-hangar/mcp-hangar-operator/issues/128)) ([a6a68bf](https://github.com/mcp-hangar/mcp-hangar-operator/commit/a6a68bfb816d114f57d6f1aff9c427303d84e2f9)), closes [#122](https://github.com/mcp-hangar/mcp-hangar-operator/issues/122)
* **controller:** reconcilers and the pod builder speak v1alpha2 ([#132](https://github.com/mcp-hangar/mcp-hangar-operator/issues/132)) ([741c4d3](https://github.com/mcp-hangar/mcp-hangar-operator/commit/741c4d3cdfc6128ef6d28c60c8217cc6e5153694))

# MCP Hangar Operator -- Kubernetes Operator (Go)

## Quick Reference

| Property | Value |
|----------|-------|
| Module | `github.com/mcp-hangar/operator` |
| Language | Go 1.26 (`go.mod`) |
| Framework | controller-runtime v0.25, k8s libraries v0.37 (kubebuilder layout) |
| CRD API version | `mcp-hangar.io/v1alpha2`, the only version served |
| Kubernetes floor | 1.30 (CI runs envtest 1.34.1 and the 1.30.3 floor, kind 1.36) |
| Linting | golangci-lint v2, pinned in the Makefile; run it with `make lint` |
| Testing | envtest + testify |
| Image | `ghcr.io/mcp-hangar/mcp-hangar-operator` |

## Commands

```bash
# Generate (CRDs, RBAC, webhook manifests, DeepCopy)
make manifests       # config/crd/bases, config/rbac/role.yaml, config/webhook
make generate        # zz_generated.deepcopy.go

# Test (envtest binaries via setup-envtest)
make test
KUBEBUILDER_ASSETS="$(setup-envtest use 1.34.1 -p path)" go test -race ./...

# Lint
make lint            # the pinned golangci-lint v2, not whatever is on PATH

# Build and run
make build           # bin/manager
make run             # against the current kubeconfig

# Deploy (kustomize, server-side apply)
make install         # CRDs
make deploy          # config/default
make undeploy
make uninstall
```

The Helm chart lives in `mcp-hangar/helm-charts` (`mcp-hangar-operator/`); it
vendors this repo's CRDs into `templates/crds/` with
`scripts/vendor-operator-crds.py` and mirrors the RBAC and webhook manifests.

## Source Layout

```
api/v1alpha2/                 # MCPServer, MCPServerGroup, MCPDiscoverySource, MCPEgressPolicy
cmd/operator/main.go          # manager, flags, the five reconcilers
internal/controller/
  mcpserver_controller.go           # pods, per-server NetworkPolicy, core health/tools
  mcpservergroup_controller.go      # status aggregation over selected servers
  mcpdiscoverysource_controller.go  # Namespace / ConfigMap / Annotations / ServiceDiscovery
  mcpegresspolicy_controller.go     # L3/L4 backstop (Vanilla or Cilium) + L7 push to core
  mcpegresspolicy_gateway.go        # re-deliver L7 policies when a gateway pod becomes Ready
  mcpegresspolicy_membership.go     # group targets
  namespace_egress_controller.go    # default-deny in enforce-egress namespaces
  conditions.go, metrics_rbac.go
internal/webhook/             # validating webhooks (off by default): MCPServer, discovery, pod registration
internal/health/              # leader-aware readiness
pkg/hangar/                   # client for core's REST API
pkg/metrics/                  # mcp_operator_* Prometheus metrics
pkg/networkpolicy/            # policy builders and the EnforcementProbe
pkg/provider/                 # provider pod builder
config/{crd,rbac,webhook,certmanager,default,manager,samples}/
test/e2e/                     # kind reachability tests (Calico, Cilium, NodeLocal DNSCache) and remote lifecycle
changelog.d/, upgrade.d/      # release-note fragments (see below)
```

## Custom Resource Definitions

- **MCPServer**: one MCP server, `mode: container` (the operator runs one pod) or
  `mode: remote` (an endpoint core reaches). `spec.replicas` is on (1) or off (0);
  there is no scale subresource. Validation that does not need a lookup is CEL on
  the CRD; `spec.mode` is immutable.
- **MCPServerGroup**: selects MCPServers by label and reports their state against
  a `healthPolicy`. A status aggregator: traffic is not routed through it.
- **MCPDiscoverySource**: creates MCPServers from namespaces, a ConfigMap,
  annotated pods/services, or services. `Additive` or `Authoritative`.
- **MCPEgressPolicy**: per-server or per-group egress allow-list, enforced at L3/L4
  by a NetworkPolicy (`Vanilla`) or CiliumNetworkPolicy (`Cilium`, needs a running
  cilium agent), and at L7 by core (tool/argument rules pushed over `--hangar-url`).
  `targetRef` is immutable.

Idle stop, circuit breaking and tool allow-lists are **core** settings
(`config.yaml` / REST), not `MCPServer` fields. Do not add CR fields for them.

## What the operator enforces

- A per-server NetworkPolicy from `spec.capabilities.network`, and a namespace
  default-deny where the namespace is labelled `mcp-hangar.io/enforce-egress=true`.
- In such a namespace an unpinned image gets no egress (pin coupling).
- Status reports enforcement only when the EnforcementProbe observes an enforcer
  (`NetworkPolicyApplied`, `BackstopEnforceable`); otherwise False or Unknown.
- Admission (webhooks, opt-in): registered provider pods, digest policy, the
  unrestricted-egress annotation.
- Capability-violation events and `mcp_operator_capability_violations_total`.

Claims in status, events and docs must match what the mechanism delivers: a
written NetworkPolicy is not an enforced one.

## Capability Declaration

```yaml
apiVersion: mcp-hangar.io/v1alpha2
kind: MCPServer
metadata:
  name: math-server
spec:
  mode: container
  image: ghcr.io/example/math-mcp@sha256:...
  capabilities:
    enforcementMode: block      # alert | block | quarantine
    network:                    # feeds the per-server NetworkPolicy
      egress:
        - host: "api.example.com"
          cidr: "203.0.113.0/24"
          port: 443
    tools:                      # drives capability-violation events
      maxCount: 10
      expectedTools: [calculate]
```

A host with no `cidr` emits no allow rule (fails closed); hostname egress is
enforced through an `MCPEgressPolicy` with the Cilium flavor.
`filesystem`, `environment` and `resources` capability children are gone (#121):
do not re-add a declaration nothing enforces.

## CRD and RBAC development

1. Edit `api/v1alpha2/*_types.go` or the `+kubebuilder` markers.
2. `make manifests generate`; commit `config/crd/bases`, `config/rbac/role.yaml`,
   `config/webhook/manifests.yaml` and `zz_generated.deepcopy.go`.
3. A new CEL rule must compile on the 1.30 floor (the `test-k8s-floor` job checks).
4. The chart picks the CRDs up by re-vendoring from the release; RBAC and webhook
   changes need a companion helm-charts PR.

## Testing conventions

- envtest for anything touching admission, CRD validation or watches (the suite
  in `internal/controller/suite_test.go` loads the generated CRDs); the fake
  client for pure reconcile logic.
- A fix ships with a test that fails without it.
- testify assertions; `*_test.go` beside the source.

## Changelog and upgrade notes

Same mechanism as mcp-hangar core. Never edit `CHANGELOG.md` or `UPGRADE.md`
directly: both are assembled on the release PR.

- Every non-trivial PR adds **one new file** `changelog.d/<id>-<slug>.<kind>.md`
  (`kind`: `added`, `changed`, `deprecated`, `removed`, `fixed`, `security`).
  `chore(deps)`, `ci`, `style`, `test` and pure `docs` PRs are exempt. The
  `changelog / check` workflow enforces it. See `changelog.d/README.md`.
- A change a reader has to act on also adds **one new file**
  `upgrade.d/<id>-<slug>.md` whose first line is `### <headline>`. Do not add an
  `## Unreleased` section to `UPGRADE.md`. See `upgrade.d/README.md`.

## What NOT to Do

- No `panic()` in production paths -- return errors
- No hardcoded image tags -- use spec fields
- No direct kubectl/exec calls -- use controller-runtime client
- No blocking reconciliation -- use requeue with backoff
- No emoji in code, comments, or documentation
- Do not edit `zz_generated.deepcopy.go` manually


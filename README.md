# MCP-Hangar Kubernetes Operator

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.25+-326CE5.svg)](https://kubernetes.io)

**Kubernetes Operator for managing MCP (Model Context Protocol) providers as native Kubernetes resources.**

## Features

- **MCPServer CRD**: Declarative management of MCP tool servers
- **MCPServerGroup CRD**: Label-selected aggregation of `MCPServer` state against a `healthPolicy`
- **MCPDiscoverySource CRD**: Automatic server discovery from namespaces
- **State Machine**: Automatic lifecycle management (Cold → Initializing → Ready → Degraded → Dead)
- **Health**: Hangar's health endpoint for `remote` servers and pod phase for `container`, on the reconcile interval
- **Metrics**: Prometheus metrics for monitoring
- **Secure by Default**: Pod security contexts, non-root, read-only filesystem
- **Policy Enforcement**: Opt-in namespaces get default-deny egress, admission-time registration, and image-pin coupling (see [Enforcement](#enforcement))

## Quick Start

### Prerequisites

- Kubernetes 1.25+
- Helm 3.x
- kubectl configured for your cluster

### Installation

The chart version is owned by release-please and tracked in the
[release compatibility matrix](https://github.com/mcp-hangar/docs/blob/main/operations/RELEASE_COMPATIBILITY.md) —
check there for the current, verified version before installing.

```bash
# Install via OCI registry (pin --version to the current chart version from
# the compatibility matrix above)
helm install mcp-hangar-operator oci://ghcr.io/mcp-hangar/charts/mcp-hangar-operator \
  --version <chart-version> \
  --namespace mcp-hangar \
  --create-namespace
```

### Create Your First Server

```yaml
apiVersion: mcp-hangar.io/v1alpha2
kind: MCPServer
metadata:
  name: my-tools
  namespace: default
spec:
  mode: container
  image: ghcr.io/my-org/my-mcp-tools:latest
  replicas: 1
  resources:
    requests:
      memory: "128Mi"
      cpu: "100m"
```

```bash
kubectl apply -f my-server.yaml
kubectl get mcpservers
```

## CRD Reference

The operator is the **deploy-time admission plane**, not Hangar's control plane.
Idle stop, circuit breaking and tool allow-lists are core settings
(`config.yaml` / the REST API) and are not `MCPServer` fields — a cluster server
discovered by Hangar takes core's own `idle_ttl_s` default. The tables below
list what the operator actually reads.

### MCPServer

| Field | Description | Default |
|-------|-------------|---------|
| `spec.mode` | Execution mode: `container` or `remote` | Required |
| `spec.image` | Container image (for container mode) | Required for container |
| `spec.endpoint` | HTTP endpoint (for remote mode) | Required for remote |
| `spec.replicas` | Number of replicas (0 = cold start) | `1` |
| `spec.startupTimeout` | How long to wait for the server to come up | `30s` |
| `spec.shutdownGracePeriod` | Pod termination grace period | `30s` |
| `spec.capabilities.network` | Egress the server declares; feeds the generated `NetworkPolicy` | — |
| `spec.capabilities.tools` | `maxCount` / `expectedTools`; drives capability-violation events | — |
| `spec.capabilities.enforcementMode` | What a violation does (`audit` / `block`) | — |

### MCPServerGroup

A **status aggregator**, not a load balancer: it selects `MCPServer`s, counts
their states, and reports Ready/Degraded/Available against a `healthPolicy`.
Traffic is not routed through it.

| Field | Description | Default |
|-------|-------------|---------|
| `spec.selector` | Label selector for member servers | Required |
| `spec.healthPolicy.minHealthyPercentage` | Minimum healthy members | `50` |
| `spec.healthPolicy.minHealthyCount` | Minimum healthy members, absolute | — |

### MCPDiscoverySource

| Field | Description | Default |
|-------|-------------|---------|
| `spec.type` | Discovery type: Namespace, ConfigMap, Annotations | Required |
| `spec.mode` | Discovery mode: Additive, Authoritative | `Additive` |
| `spec.refreshInterval` | Rescan interval | `1m` |

## Enforcement

The operator can make *the governed path the default path*: in an opted-in
namespace an MCP server only reaches the network once it is **registered** and
its image is **pinned**. Enforcement is **opt-in per namespace** — label the
namespace to turn it on, so it never gates workloads elsewhere:

```bash
kubectl label namespace my-mcp mcp-hangar.io/enforce-egress=true
```

Inside an enforced namespace three controls apply:

| Control | Behaviour | Where |
|---------|-----------|-------|
| **Default-deny egress** | A server with no egress policy gets DNS only — not "all egress". Egress opens only when a policy is generated for it. | `pkg/networkpolicy/builder.go` |
| **Admission registration** | A Pod labelled `mcp-hangar.io/provider=<name>` is **denied at admission** unless an `MCPServer` named `<name>` exists in the namespace. Shadow/unregistered provider pods fail to deploy. | `internal/webhook/pod_registration_webhook.go` (OWASP MCP09) |
| **Pin coupling** | A registered container-mode server's egress allow-policy is opened **only if its image is digest-pinned** (`image@sha256:...`). An unpinned server stays under default-deny (DNS only) and gets an `EgressWithheldUnpinnedImage` event. Opt out per server with the `hangar.io/allow-mutable-image="true"` annotation. | `internal/controller/mcpserver_controller.go` |

FQDN/host egress rules **fail closed** — a rule that a Kubernetes
`NetworkPolicy` cannot express (host/FQDN with no CIDR) emits no permissive rule.
Declarative L7/FQDN egress is handled by the `MCPEgressPolicy` API and its
Cilium/Tetragon backstop (ADR-006).

## Examples

See [`config/samples/`](config/samples/) for complete, runnable examples:

- [`mcp-hangar_v1alpha2_mcpserver.yaml`](config/samples/mcp-hangar_v1alpha2_mcpserver.yaml) - Basic container-mode `MCPServer`. For an external endpoint provider, set `spec.mode: remote` and `spec.endpoint` instead of `spec.image`; for Secret-backed config, add `spec.env`/`spec.volumes` referencing a `Secret` (both fields are part of the `MCPServer` spec).
- [`mcp-hangar_v1alpha2_mcpservergroup.yaml`](config/samples/mcp-hangar_v1alpha2_mcpservergroup.yaml) - Group of `MCPServer`s selected by label, with a `healthPolicy` the group reports against.
- [`mcp-hangar_v1alpha2_mcpdiscoverysource.yaml`](config/samples/mcp-hangar_v1alpha2_mcpdiscoverysource.yaml) - Automatic server discovery from a namespace.

Apply all samples at once:

```bash
kubectl apply -k config/samples/
```

## Metrics

The operator exposes Prometheus metrics at `:8080/metrics`:

| Metric | Description |
|--------|-------------|
| `controller_runtime_reconcile_total` | Total reconciliations (controller-runtime built-in) |
| `controller_runtime_reconcile_time_seconds` | Reconciliation duration (controller-runtime built-in) |
| `mcp_operator_provider_state` | Provider state (1 = in state) |
| `mcp_operator_provider_tools_count` | Tools per provider |
| `mcp_operator_provider_health_check_failures_total` | Health check failures |

## Development

```bash
# Run locally
make run

# Run all tests
make test

# Run tests with coverage
go test ./... -cover

# Run specific package tests
go test ./pkg/provider -v
go test ./pkg/hangar -v
go test ./pkg/metrics -v
go test ./internal/controller -v

# Build image
make docker-build IMG=my-registry/mcp-hangar-operator:v0.1.0

# Push image
make docker-push IMG=my-registry/mcp-hangar-operator:v0.1.0
```

### Testing

- **pkg/provider**: Pod builder
- **pkg/hangar**: Hangar client
- **pkg/metrics**: Prometheus metrics
- **internal/controller**: Controller config

Run `make test` to execute the full suite with coverage (`go test ./... -coverprofile cover.out`); run `go tool cover -func cover.out` afterwards for a per-package breakdown.

## License

MIT License

---
[Docs](https://mcp-hangar.io) | [GitHub](https://github.com/mcp-hangar/mcp-hangar-operator)

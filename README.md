# MCP-Hangar Kubernetes Operator

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.25+-326CE5.svg)](https://kubernetes.io)

**Kubernetes Operator for managing MCP (Model Context Protocol) providers as native Kubernetes resources.**

## Features

- **MCPProvider CRD**: Declarative management of MCP tool providers
- **MCPProviderGroup CRD**: Load balancing and high availability for provider groups
- **MCPDiscoverySource CRD**: Automatic provider discovery from namespaces
- **State Machine**: Automatic lifecycle management (Cold → Initializing → Ready → Degraded → Dead)
- **Health Checks**: Configurable health monitoring with circuit breaker
- **Metrics**: Prometheus metrics for monitoring
- **Secure by Default**: Pod security contexts, non-root, read-only filesystem

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

### Create Your First Provider

```yaml
apiVersion: mcp-hangar.io/v1alpha1
kind: MCPProvider
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
kubectl apply -f my-provider.yaml
kubectl get mcpproviders
```

## CRD Reference

### MCPProvider

| Field | Description | Default |
|-------|-------------|---------|
| `spec.mode` | Execution mode: `container` or `remote` | Required |
| `spec.image` | Container image (for container mode) | Required for container |
| `spec.endpoint` | HTTP endpoint (for remote mode) | Required for remote |
| `spec.replicas` | Number of replicas (0 = cold start) | `1` |
| `spec.idleTTL` | Idle timeout before shutdown | `5m` |
| `spec.healthCheck.enabled` | Enable health checks | `true` |
| `spec.healthCheck.interval` | Health check interval | `30s` |
| `spec.circuitBreaker.enabled` | Enable circuit breaker | `true` |

### MCPProviderGroup

| Field | Description | Default |
|-------|-------------|---------|
| `spec.selector` | Label selector for providers | Required |
| `spec.strategy` | Load balancing: RoundRobin, LeastConnections, Random, Failover | `RoundRobin` |
| `spec.failover.maxRetries` | Maximum retry attempts | `2` |
| `spec.healthPolicy.minHealthyPercentage` | Minimum healthy providers | `50` |

### MCPDiscoverySource

| Field | Description | Default |
|-------|-------------|---------|
| `spec.type` | Discovery type: Namespace, ConfigMap, Annotations | Required |
| `spec.mode` | Discovery mode: Additive, Authoritative | `Additive` |
| `spec.refreshInterval` | Rescan interval | `1m` |

## Examples

See [`config/samples/`](config/samples/) for complete, runnable examples:

- [`mcp-hangar_v1alpha2_mcpserver.yaml`](config/samples/mcp-hangar_v1alpha2_mcpserver.yaml) - Basic container-mode `MCPServer`. For an external endpoint provider, set `spec.mode: remote` and `spec.endpoint` instead of `spec.image`; for Secret-backed config, add `spec.env`/`spec.volumes` referencing a `Secret` (both fields are part of the `MCPServer` spec).
- [`mcp-hangar_v1alpha2_mcpservergroup.yaml`](config/samples/mcp-hangar_v1alpha2_mcpservergroup.yaml) - High-availability group of `MCPServer`s load-balanced with the `RoundRobin` strategy.
- [`mcp-hangar_v1alpha2_mcpdiscoverysource.yaml`](config/samples/mcp-hangar_v1alpha2_mcpdiscoverysource.yaml) - Automatic provider discovery from a namespace.

Apply all samples at once:

```bash
kubectl apply -k config/samples/
```

## Metrics

The operator exposes Prometheus metrics at `:8080/metrics`:

| Metric | Description |
|--------|-------------|
| `mcp_operator_reconcile_total` | Total reconciliations |
| `mcp_operator_reconcile_duration_seconds` | Reconciliation duration |
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

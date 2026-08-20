# MCP Hangar Operator -- Kubernetes Operator (Go)

## Quick Reference

| Property | Value |
|----------|-------|
| Module | `github.com/mcp-hangar/operator` |
| Language | Go 1.23 |
| Framework | controller-runtime v0.17 (kubebuilder) |
| CRD API version | `mcp-hangar.io/v1alpha2` (storage; v1alpha1 still served, converted) |
| Linting | golangci-lint v1.55 |
| Testing | envtest + testify + gomega |
| Image | `ghcr.io/mcp-hangar/mcp-hangar-operator` |

## Commands

```bash

# Setup
go mod download

# Generate (CRDs, DeepCopy, RBAC)
make manifests       # WebhookConfiguration, ClusterRole, CRDs
make generate        # DeepCopy, DeepCopyInto, DeepCopyObject

# Test
make test            # full test suite (manifests + generate + fmt + vet + envtest)
go test ./...        # quick test run (skips generation)

# Lint
make lint            # golangci-lint
go vet ./...         # go vet only

# Build
make build           # binary -> bin/manager
go build -o bin/manager cmd/operator/main.go

# Run locally
make run             # runs controller against current kubeconfig

# Docker
make docker-build    # build image
make docker-push     # push image

# Deploy to cluster
make install         # install CRDs
make deploy          # deploy controller via Helm
make undeploy        # remove controller
make uninstall       # remove CRDs
```

## Source Layout

```
operator/
├── api/
│   ├── v1alpha1/                  # served, converted to the hub
│   │   ├── groupversion_info.go   # SchemeBuilder, GroupVersion
│   │   ├── conversion.go          # hand-written conversion to/from v1alpha2
│   │   ├── mcpserver_types.go     # MCPServer CRD
│   │   ├── mcpservergroup_types.go # MCPServerGroup CRD
│   │   ├── mcpdiscoverysource_types.go # MCPDiscoverySource CRD
│   │   └── zz_generated.deepcopy.go   # Generated -- do not edit
│   └── v1alpha2/                  # STORAGE version (the hub)
│       ├── mcpserver_types.go, mcpservergroup_types.go
│       ├── mcpdiscoverysource_types.go, mcpegresspolicy_types.go
│       └── zz_generated.deepcopy.go   # Generated -- do not edit
│
├── cmd/
│   └── operator/
│       └── main.go                # Entrypoint
│
├── internal/
│   └── controller/                # Reconciliation controllers
│       ├── mcpserver_controller.go
│       ├── mcpservergroup_controller.go
│       ├── mcpdiscoverysource_controller.go
│       ├── mcpegresspolicy_controller.go
│       ├── suite_test.go          # envtest setup (TestMain)
│       ├── admission_test.go      # CRD schema bounds, against a real apiserver
│       └── *_controller_test.go
│
├── pkg/
│   ├── hangar/                    # Client for MCP Hangar core
│   ├── metrics/                   # Prometheus metrics
│   │   ├── metrics.go
│   │   └── metrics_test.go
│   └── provider/                  # Provider lifecycle management
│
├── config/
│   └── crd/
│       └── bases/                 # Generated CRD manifests
│
├── hack/
│   └── boilerplate.go.txt         # License header for generated files
│
├── Makefile                       # Build, test, deploy targets
├── Dockerfile
├── go.mod
└── go.sum
```

## Custom Resource Definitions

### MCPServer

Manages an individual MCP server's lifecycle in Kubernetes.

```yaml
apiVersion: mcp-hangar.io/v1alpha2
kind: MCPServer
metadata:
  name: math-server
spec:
  mode: container           # container | remote
  image: ghcr.io/example/math-mcp:latest
  replicas: 1
  startupTimeout: "30s"
  shutdownGracePeriod: "30s"
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "256Mi"
```

Idle stop, circuit breaking and tool allow-lists are **core** settings
(`config.yaml` / REST), not `MCPServer` fields. The operator is the deploy-time
admission plane; do not add CR fields for them, and do not teach the operator to
call those APIs.

### MCPServerGroup

Selects `MCPServer`s by label, counts their states and reports Ready/Degraded/
Available against a `healthPolicy`. It is a status aggregator — **traffic is not
routed through it**, and there is no strategy or failover to configure.

### MCPDiscoverySource

Configures automatic server discovery.

## Architecture

### Reconciliation Loop

Each controller follows the standard controller-runtime reconciliation pattern:

1. Observe current state (Get resource from API server)
2. Compute desired state (based on spec)
3. Act to converge (Create/Update/Delete child resources)
4. Update status (conditions, observed generation)

### Provider State Machine

Maps to the core Python state machine:

| State | Description |
|-------|-------------|
| `Cold` | Not running |
| `Initializing` | Starting up |
| `Ready` | Healthy, serving tools |
| `Degraded` | Unhealthy, needs reinit |
| `Dead` | Failed, may retry |

### Status Conditions

Use standard Kubernetes condition pattern:

```go
meta.SetStatusCondition(&provider.Status.Conditions, metav1.Condition{
    Type:               "Ready",
    Status:             metav1.ConditionTrue,
    Reason:             "ProviderReady",
    Message:            "Provider is ready to serve tools",
    ObservedGeneration: provider.Generation,
})
```

## Code Conventions

### Go Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `golangci-lint` for additional checks
- Error wrapping: `fmt.Errorf("failed to start provider: %w", err)`
- Context propagation: always pass `ctx context.Context` as first argument
- No `panic()` in controller code -- return errors

### Testing

- Use `envtest` for integration tests with real API server
- Use `testify` assertions: `assert.Equal(t, expected, actual)`
- Table-driven tests for multiple scenarios
- Test file naming: `*_test.go` alongside source

```go
func TestMCPServerReconcile_CreatesReadyServer(t *testing.T) {
    // Arrange
    server := &mcpv1alpha2.MCPServer{...}
    // Act
    result, err := reconciler.Reconcile(ctx, ctrl.Request{...})
    // Assert
    assert.NoError(t, err)
    assert.Equal(t, ctrl.Result{}, result)
}
```

### CRD Development

When modifying CRD types in `api/v1alpha1/`:

1. Edit `*_types.go` files
2. Run `make generate` (regenerates `zz_generated.deepcopy.go`)
3. Run `make manifests` (regenerates CRD YAML in `config/crd/bases/`)
4. Copy CRD manifests to `../helm-charts/mcp-hangar-operator/crds/` if needed
5. Run `make test` to verify

### Kubebuilder Markers

Use kubebuilder markers for CRD validation:

```go
// +kubebuilder:validation:Enum=container;remote
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=0
// +kubebuilder:validation:Maximum=10
// +kubebuilder:default=1
// +optional
```

## Metrics

Prometheus metrics exposed via controller-runtime metrics server:

| Metric | Type | Labels |
|--------|------|--------|
| `controller_runtime_reconcile_total` | Counter | controller, result (controller-runtime built-in) |
| `controller_runtime_reconcile_time_seconds` | Histogram | controller (controller-runtime built-in) |
| `mcp_operator_provider_state` | Gauge | provider, namespace, state |

## Dependencies on Other Subprojects

- **helm-charts**: CRD manifests from `config/crd/bases/` copied to `../helm-charts/mcp-hangar-operator/crds/`
- **core**: Operator communicates with running MCP Hangar instances via HTTP API

## Hardening Priorities (v0.13.0 -- Phase 1)

The operator is the **primary enforcement engine** for Kubernetes-deployed MCP servers. These are the P0/P1 items from the product roadmap:

### P0 -- Must have for v0.13.0

| Item | Current State | Target State |
|------|---------------|--------------|
| **NetworkPolicy generation** | Not implemented | Auto-generate from CRD `capabilities` field; default-deny egress |
| **Violation signaling** | Not implemented | First-class `violation` and `enforcement` events from operator decisions |
| **CRD validation** | Basic | CEL validation rules, webhook admission |
| **Admission/policy integration** | Minimal | Validate and reject unsafe provider specs before runtime |
| **Operator enforcement loop** | Reconciles state only | Full governance posture: capability enforcement, NetworkPolicy rollout, violation signaling |
| **Pod Security Standards** | Partial (security context) | Enforce `restricted` PSS by default |

### P1 -- Important

| Item | Current State | Target State |
|------|---------------|--------------|
| **RBAC scoping** | Cluster-wide | Namespace-scoped with aggregated ClusterRoles |
| **Operator HA** | Leader election exists | Anti-affinity, PDB, multi-replica |
| **Helm chart hardening** | Basic | CIS benchmark aligned, OPA/Kyverno policies shipped |

### P2 -- H2 2026

| Item | Target |
|------|--------|
| **Upgrade strategy** | CRD versioning, conversion webhooks, migration guide |

## Capability Declaration

`spec.capabilities` declares what a server needs, and the operator acts on two
parts of it:

```yaml
apiVersion: mcp-hangar.io/v1alpha2
kind: MCPServer
metadata:
  name: math-server
spec:
  mode: container
  image: ghcr.io/example/math-mcp:latest
  capabilities:
    enforcementMode: block      # audit | block -- what a violation does
    network:                    # feeds the generated NetworkPolicy
      egress:
        - host: "api.example.com"
          port: 443
        - cidr: "10.0.0.0/8"
          port: 5432
    tools:                      # drives capability-violation events
      maxCount: 10
      expectedTools: [calculate]
```

The operator uses this block to:

1. Generate NetworkPolicy resources (default-deny egress + explicit allow-list)
2. Enforce Pod Security Standards on generated pods
3. Emit violation events when the running tool set does not match the declaration

**`filesystem`, `environment` and `resources` children are gone** (#121). They
were the Tetragon / hangar-agent path, retired by ADR-010, and nothing read
them. Do not re-add them: a declaration nothing enforces reads as enforcement.

## What NOT to Do

- No `panic()` in production paths -- return errors
- No hardcoded image tags -- use spec fields
- No direct kubectl/exec calls -- use controller-runtime client
- No blocking reconciliation -- use requeue with backoff
- No emoji in code, comments, or documentation
- Do not edit `zz_generated.deepcopy.go` manually


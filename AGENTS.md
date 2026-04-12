# MCP Hangar Operator -- Kubernetes Operator (Go)

## Quick Reference

| Property | Value |
|----------|-------|
| Module | `github.com/mcp-hangar/operator` |
| Language | Go 1.23 |
| Framework | controller-runtime v0.17 (kubebuilder) |
| CRD API version | `mcp-hangar.io/v1alpha1` |
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
│   └── v1alpha1/                  # CRD type definitions
│       ├── groupversion_info.go   # SchemeBuilder, GroupVersion
│       ├── mcpprovider_types.go   # MCPProvider CRD
│       ├── mcpprovidergroup_types.go  # MCPProviderGroup CRD
│       ├── mcpdiscoverysource_types.go # MCPDiscoverySource CRD
│       └── zz_generated.deepcopy.go   # Generated -- do not edit
│
├── cmd/
│   └── operator/
│       └── main.go                # Entrypoint
│
├── internal/
│   └── controller/                # Reconciliation controllers
│       ├── mcpprovider_controller.go
│       ├── mcpprovidergroup_controller.go
│       ├── mcpdiscoverysource_controller.go
│       ├── suite_test.go          # envtest setup (TestMain)
│       ├── controller_test.go
│       ├── mcpprovidergroup_controller_test.go
│       └── mcpdiscoverysource_controller_test.go
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

### MCPProvider

Manages individual MCP provider lifecycle in Kubernetes.

```yaml
apiVersion: mcp-hangar.io/v1alpha1
kind: MCPProvider
metadata:
  name: math-provider
spec:
  mode: container           # container | remote
  image: ghcr.io/example/math-mcp:latest
  replicas: 1
  idleTTL: "5m"
  startupTimeout: "30s"
  healthCheck:
    enabled: true
    interval: "30s"
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "256Mi"
```

### MCPProviderGroup

Groups providers for load balancing and failover.

### MCPDiscoverySource

Configures automatic provider discovery.

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
func TestMCPProviderReconcile_CreatesReadyProvider(t *testing.T) {
    // Arrange
    provider := &mcpv1alpha1.MCPProvider{...}
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
| `mcp_operator_reconcile_total` | Counter | controller, result |
| `mcp_operator_reconcile_duration_seconds` | Histogram | controller |
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

## Capability Declaration (v0.13.0)

The MCPProvider CRD must be extended with a `capabilities` block declaring what each server needs:

```yaml
apiVersion: mcp-hangar.io/v1alpha1
kind: MCPProvider
metadata:
  name: math-provider
spec:
  mode: container
  image: ghcr.io/example/math-mcp:latest
  capabilities:
    network:
      egress:
        - host: "api.example.com"
          ports: [443]
        - cidr: "10.0.0.0/8"
          ports: [5432]
    filesystem:
      readOnly: true
      mounts:
        - path: /data
          readOnly: false
    environment:
      - NAME_PATTERN  # Allowed env var patterns
    tools:
      expected:
        - name: calculate
          parameters: [expression]
```

The operator uses this block to:
1. Generate NetworkPolicy resources (default-deny egress + explicit allowlist)
2. Enforce Pod Security Standards on generated pods
3. Verify runtime behavior matches declarations
4. Emit violation events on drift

## What NOT to Do

- No `panic()` in production paths -- return errors
- No hardcoded image tags -- use spec fields
- No direct kubectl/exec calls -- use controller-runtime client
- No blocking reconciliation -- use requeue with backoff
- No emoji in code, comments, or documentation
- Do not edit `zz_generated.deepcopy.go` manually


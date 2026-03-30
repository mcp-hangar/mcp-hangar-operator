package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	"github.com/mcp-hangar/operator/internal/webhook"
)

func newProvider(name string, mode mcpv1alpha1.ProviderMode) *mcpv1alpha1.MCPProvider {
	return &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode: mode,
		},
	}
}

func int32Ptr(i int32) *int32 { return &i }

// ── Container mode ────────────────────────────────────────────────────

func TestValidateCreate_ContainerMode_Valid(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("valid-container", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "ghcr.io/test/provider:latest"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateCreate_ContainerMode_MissingImage(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("no-image", mcpv1alpha1.ProviderModeContainer)

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.image is required")
}

func TestValidateCreate_ContainerMode_EndpointWarning(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("container-with-endpoint", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Endpoint = "http://ignored.example.com"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "spec.endpoint is ignored")
}

// ── Remote mode ────────────────────────────────────────────────────────

func TestValidateCreate_RemoteMode_Valid(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("valid-remote", mcpv1alpha1.ProviderModeRemote)
	p.Spec.Endpoint = "https://api.example.com/mcp"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateCreate_RemoteMode_MissingEndpoint(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("no-endpoint", mcpv1alpha1.ProviderModeRemote)

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.endpoint is required")
}

func TestValidateCreate_RemoteMode_InvalidEndpoint(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("bad-endpoint", mcpv1alpha1.ProviderModeRemote)
	p.Spec.Endpoint = "not-a-url"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid URL")
}

func TestValidateCreate_RemoteMode_ImageWarning(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("remote-with-image", mcpv1alpha1.ProviderModeRemote)
	p.Spec.Endpoint = "https://api.example.com"
	p.Spec.Image = "test:latest"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "spec.image is ignored")
}

// ── Duration validation ────────────────────────────────────────────────

func TestValidateCreate_InvalidDuration(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("bad-duration", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.IdleTTL = "not-a-duration"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.idleTTL")
	assert.Contains(t, err.Error(), "not a valid duration")
}

func TestValidateCreate_NegativeDuration(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("negative-duration", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.StartupTimeout = "-5s"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.startupTimeout")
	assert.Contains(t, err.Error(), "must not be negative")
}

func TestValidateCreate_ValidDurations(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("valid-durations", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.IdleTTL = "10m"
	p.Spec.StartupTimeout = "1m30s"
	p.Spec.ShutdownGracePeriod = "45s"
	p.Spec.HealthCheck = &mcpv1alpha1.HealthCheckConfig{
		Interval: "30s",
		Timeout:  "5s",
	}
	p.Spec.CircuitBreaker = &mcpv1alpha1.CircuitBreakerConfig{
		ResetTimeout: "1m",
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateCreate_HealthCheckInvalidDuration(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("bad-hc-duration", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.HealthCheck = &mcpv1alpha1.HealthCheckConfig{
		Interval: "abc",
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.healthCheck.interval")
}

// ── Tools validation ──────────────────────────────────────────────────

func TestValidateCreate_ToolsAllowAndDenyMutuallyExclusive(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("both-lists", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Tools = &mcpv1alpha1.ToolsConfig{
		AllowList: []string{"calc"},
		DenyList:  []string{"exec"},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestValidateCreate_ToolsAllowListOnly(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("allow-only", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Tools = &mcpv1alpha1.ToolsConfig{
		AllowList: []string{"calc", "convert"},
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// ── Capabilities validation ──────────────────────────────────────────

func TestValidateCreate_DuplicateExpectedTools(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("dup-tools", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		Tools: &mcpv1alpha1.ToolCapabilitiesSpec{
			ExpectedTools: []string{"calc", "convert", "calc"},
		},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "calc")
}

func TestValidateCreate_EmptyExpectedTool(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("empty-tool", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		Tools: &mcpv1alpha1.ToolCapabilitiesSpec{
			ExpectedTools: []string{"calc", ""},
		},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty string")
}

func TestValidateCreate_EgressMissingHostAndCIDR(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("empty-egress-rule", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha1.EgressRuleSpec{
				{Port: 443},
			},
		},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host or cidr must be set")
}

func TestValidateCreate_EgressValidCIDR(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("cidr-egress", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha1.EgressRuleSpec{
				{CIDR: "10.0.0.0/8", Port: 5432},
			},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateCreate_ValidCapabilities(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("full-caps", mcpv1alpha1.ProviderModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		EnforcementMode: "block",
		Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha1.EgressRuleSpec{
				{Host: "api.example.com", Port: 443, Protocol: "https"},
				{CIDR: "10.0.0.0/8", Port: 5432, Protocol: "tcp"},
			},
		},
		Tools: &mcpv1alpha1.ToolCapabilitiesSpec{
			MaxCount:      10,
			ExpectedTools: []string{"calculate", "convert", "lookup"},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// ── Update validation ─────────────────────────────────────────────────

func TestValidateUpdate_Valid(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	old := newProvider("update-test", mcpv1alpha1.ProviderModeContainer)
	old.Spec.Image = "test:v1"

	updated := old.DeepCopy()
	updated.Spec.Image = "test:v2"

	warnings, err := v.ValidateUpdate(context.Background(), old, updated)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateUpdate_InvalidNewSpec(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	old := newProvider("update-invalid", mcpv1alpha1.ProviderModeContainer)
	old.Spec.Image = "test:v1"

	updated := old.DeepCopy()
	updated.Spec.Image = ""

	_, err := v.ValidateUpdate(context.Background(), old, updated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.image is required")
}

// ── Delete validation ─────────────────────────────────────────────────

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("delete-me", mcpv1alpha1.ProviderModeContainer)

	warnings, err := v.ValidateDelete(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// ── Multiple errors ───────────────────────────────────────────────────

func TestValidateCreate_MultipleErrors(t *testing.T) {
	v := &webhook.MCPProviderValidator{}
	p := newProvider("multi-error", mcpv1alpha1.ProviderModeContainer)
	// Missing image + bad duration + duplicate tools
	p.Spec.IdleTTL = "xyz"
	p.Spec.Capabilities = &mcpv1alpha1.ProviderCapabilities{
		Tools: &mcpv1alpha1.ToolCapabilitiesSpec{
			ExpectedTools: []string{"a", "a"},
		},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	errMsg := err.Error()
	assert.Contains(t, errMsg, "spec.image is required")
	assert.Contains(t, errMsg, "spec.idleTTL")
	assert.Contains(t, errMsg, "duplicate")
}

package webhook_test

import (
	"context"
	"testing"

	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

func newProviderV2(name string, mode mcpv1alpha2.MCPServerMode) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mode},
	}
}

func dur(d string) *metav1.Duration {
	pd, err := time.ParseDuration(d)
	if err != nil {
		panic(err)
	}
	return &metav1.Duration{Duration: pd}
}

func TestV2_ContainerMode_MissingImage(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("no-image", mcpv1alpha2.MCPServerModeContainer)

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.image is required")
}

func TestV2_ContainerMode_Valid(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("valid", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "ghcr.io/test/provider:latest"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestV2_ContainerMode_EndpointWarning(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("container-endpoint", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Endpoint = "http://ignored.example.com"

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "spec.endpoint is ignored")
}

func TestV2_RemoteMode_MissingEndpoint(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("no-endpoint", mcpv1alpha2.MCPServerModeRemote)

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.endpoint is required")
}

func TestV2_RemoteMode_InvalidEndpoint(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("bad-endpoint", mcpv1alpha2.MCPServerModeRemote)
	p.Spec.Endpoint = "not-a-url"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http or https")
}

func TestV2_NegativeDuration(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("neg-dur", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.StartupTimeout = dur("-5s")

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.startupTimeout")
	assert.Contains(t, err.Error(), "must not be negative")
}

func TestV2_ValidDurations(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("valid-dur", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.IdleTTL = dur("10m")
	p.Spec.HealthCheck = &mcpv1alpha2.HealthCheckConfig{Interval: dur("30s"), Timeout: dur("5s")}
	p.Spec.CircuitBreaker = &mcpv1alpha2.CircuitBreakerConfig{ResetTimeout: dur("1m")}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestV2_ToolsMutuallyExclusive(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("both-lists", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Tools = &mcpv1alpha2.ToolsConfig{AllowList: []string{"calc"}, DenyList: []string{"exec"}}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestV2_DuplicateExpectedTools(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("dup-tools", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Tools: &mcpv1alpha2.ToolCapabilitiesSpec{ExpectedTools: []string{"calc", "calc"}},
	}

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestV2_EgressHostOnlyWarns(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("host-egress", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "api.internal.example", Port: 443}},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "api.internal.example")
	assert.Contains(t, warnings[0], "will NOT be applied")
}

func TestV2_EgressValidCIDR(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("cidr-egress", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test:latest"
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "db.internal", CIDR: "10.0.0.0/8", Port: 5432}},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestV2_ValidateUpdate(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	old := newProviderV2("upd", mcpv1alpha2.MCPServerModeContainer)
	old.Spec.Image = "test:v1"
	updated := old.DeepCopy()
	updated.Spec.Image = ""

	_, err := v.ValidateUpdate(context.Background(), old, updated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.image is required")
}

func TestV2_ValidateDelete(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("del", mcpv1alpha2.MCPServerModeContainer)
	warnings, err := v.ValidateDelete(context.Background(), p)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestV2_WrongType(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	_, err := v.ValidateCreate(context.Background(), &mcpv1alpha2.MCPServerGroup{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected v1alpha2 MCPServer")
}

// ── Typed-nil guard (#22) ─────────────────────────────────────────────

func TestV2_TypedNilRejected(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	var p *mcpv1alpha2.MCPServer
	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
}

// ── Remote endpoint scheme (#22) ──────────────────────────────────────

func TestV2_RemoteMode_JavascriptSchemeRejected(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("js-endpoint", mcpv1alpha2.MCPServerModeRemote)
	p.Spec.Endpoint = "javascript:alert(1)"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http or https")
}

func TestV2_RemoteMode_BarePathRejected(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("bare-path", mcpv1alpha2.MCPServerModeRemote)
	p.Spec.Endpoint = "/only/path"

	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
}

func TestV2_RemoteMode_HTTPAccepted(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("http-ep", mcpv1alpha2.MCPServerModeRemote)
	p.Spec.Endpoint = "https://api.example.com/mcp"

	_, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
}

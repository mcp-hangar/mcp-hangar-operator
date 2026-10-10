package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// ── MCPDiscoverySource (v1alpha2) ─────────────────────────────────────


func TestDiscoveryV2_InvalidIncludeRegexp(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:    mcpv1alpha2.DiscoveryTypeNamespace,
			Filters: &mcpv1alpha2.DiscoveryFilters{IncludePatterns: []string{"["}},
		},
	}

	_, err := v.ValidateCreate(context.Background(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "includePatterns[0]")
	assert.Contains(t, err.Error(), "not a valid regexp")
}

func TestDiscoveryV2_Valid(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "providers"},
			Filters:      &mcpv1alpha2.DiscoveryFilters{IncludePatterns: []string{"^prod-.*$"}, ExcludePatterns: []string{"test"}},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), d)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// ── MCPDiscoverySource (v1alpha1) ─────────────────────────────────────

func TestDiscoveryV2_DeleteAllowed(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	d := &mcpv1alpha2.MCPDiscoverySource{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"}}
	warnings, err := v.ValidateDelete(context.Background(), d)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// ── Typed-nil guards (#22) ────────────────────────────────────────────

func TestDiscoveryV2_TypedNilRejected(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	var d *mcpv1alpha2.MCPDiscoverySource
	_, err := v.ValidateCreate(context.Background(), d)
	require.Error(t, err)
}

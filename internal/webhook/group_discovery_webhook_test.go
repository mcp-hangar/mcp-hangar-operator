package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// ── MCPServerGroup (v1alpha2) ─────────────────────────────────────────

func TestGroupV2_HeaderAffinityRequiresHeader(t *testing.T) {
	v := &webhook.MCPServerGroupV1alpha2Validator{}
	g := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector:        &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			SessionAffinity: &mcpv1alpha2.SessionAffinityConfig{Type: "Header"},
		},
	}

	_, err := v.ValidateCreate(context.Background(), g)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.sessionAffinity.header is required")
}

func TestGroupV2_Valid(t *testing.T) {
	v := &webhook.MCPServerGroupV1alpha2Validator{}
	g := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector:        &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			SessionAffinity: &mcpv1alpha2.SessionAffinityConfig{Type: "Header", Header: "X-Session"},
		},
	}

	warnings, err := v.ValidateCreate(context.Background(), g)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestGroupV2_MissingSelector(t *testing.T) {
	v := &webhook.MCPServerGroupV1alpha2Validator{}
	g := &mcpv1alpha2.MCPServerGroup{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"}}

	_, err := v.ValidateUpdate(context.Background(), g, g)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.selector is required")
}

// ── MCPServerGroup (v1alpha1) ─────────────────────────────────────────

func TestGroupV1_HeaderAffinityRequiresHeader(t *testing.T) {
	v := &webhook.MCPServerGroupValidator{}
	g := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector:        &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			SessionAffinity: &mcpv1alpha1.SessionAffinityConfig{Type: "Header"},
		},
	}

	_, err := v.ValidateCreate(context.Background(), g)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.sessionAffinity.header is required")
}

// ── MCPDiscoverySource (v1alpha2) ─────────────────────────────────────

func TestDiscoveryV2_ConfigMapTypeRequiresRef(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"},
		Spec:       mcpv1alpha2.MCPDiscoverySourceSpec{Type: mcpv1alpha2.DiscoveryTypeConfigMap},
	}

	_, err := v.ValidateCreate(context.Background(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.configMapRef is required")
}

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

func TestDiscoveryV1_InvalidExcludeRegexp(t *testing.T) {
	v := &webhook.MCPDiscoverySourceValidator{}
	d := &mcpv1alpha1.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"},
		Spec: mcpv1alpha1.MCPDiscoverySourceSpec{
			Type:    mcpv1alpha1.DiscoveryTypeNamespace,
			Filters: &mcpv1alpha1.DiscoveryFilters{ExcludePatterns: []string{"(unclosed"}},
		},
	}

	_, err := v.ValidateCreate(context.Background(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "excludePatterns[0]")
}

func TestDiscoveryV2_DeleteAllowed(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	d := &mcpv1alpha2.MCPDiscoverySource{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"}}
	warnings, err := v.ValidateDelete(context.Background(), d)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

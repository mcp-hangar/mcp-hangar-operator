package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// Wildcard-egress admission moved from a (uninstallable) CRD CEL rule into this
// webhook (#54). These exercise the real ValidateCreate path -- not a Go mirror
// of the CEL -- so they fail if the enforcement regresses.

func wildcardProvider(name string) *mcpv1alpha1.MCPServer {
	p := newProvider(name, mcpv1alpha1.MCPServerModeContainer)
	p.Spec.Image = "ghcr.io/test/provider:latest"
	p.Spec.Capabilities = &mcpv1alpha1.MCPServerCapabilities{
		Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha1.EgressRuleSpec{{Host: "*", Port: 443, Protocol: "https"}},
		},
	}
	return p
}

func TestValidateProvider_WildcardEgress_RejectedWithoutAnnotation(t *testing.T) {
	v := &webhook.MCPServerValidator{}
	_, err := v.ValidateCreate(context.Background(), wildcardProvider("wildcard-no-ann"))
	require.Error(t, err, "wildcard egress without the opt-in annotation must be rejected")
	assert.Contains(t, err.Error(), "allow-unrestricted-egress")
}

func TestValidateProvider_WildcardEgress_AcceptedWithOverride(t *testing.T) {
	v := &webhook.MCPServerValidator{}
	p := wildcardProvider("wildcard-override")
	p.Annotations = map[string]string{"hangar.io/allow-unrestricted-egress": "true"}
	_, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err, "wildcard egress with the opt-in annotation must be accepted")
}

func TestValidateProvider_WildcardEgress_RejectedWithWrongAnnotationValue(t *testing.T) {
	v := &webhook.MCPServerValidator{}
	p := wildcardProvider("wildcard-wrong-value")
	p.Annotations = map[string]string{"hangar.io/allow-unrestricted-egress": "yes"}
	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err, "the annotation must equal \"true\"; other values are rejected")
	assert.Contains(t, err.Error(), "allow-unrestricted-egress")
}

func TestValidateProvider_NonWildcardEgress_Accepted(t *testing.T) {
	v := &webhook.MCPServerValidator{}
	p := newProvider("specific-egress", mcpv1alpha1.MCPServerModeContainer)
	p.Spec.Image = "ghcr.io/test/provider:latest"
	p.Spec.Capabilities = &mcpv1alpha1.MCPServerCapabilities{
		Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha1.EgressRuleSpec{{Host: "api.example.com", CIDR: "10.0.0.0/8", Port: 443}},
		},
	}
	_, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err, "a specific (non-wildcard) egress host needs no annotation")
}

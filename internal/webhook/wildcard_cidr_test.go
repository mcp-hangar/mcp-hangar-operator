package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// The opt-in gate for unrestricted egress keyed on host "*" only, so a rule
// with cidr 0.0.0.0/0 or ::/0 opened every destination without the annotation
// (#214).
func TestV2_ZeroPrefixCIDRNeedsTheUnrestrictedAnnotation(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	for _, rule := range []mcpv1alpha2.EgressRuleSpec{
		{Host: "api.example.com", CIDR: "0.0.0.0/0", Port: 443},
		{Host: "api.example.com", CIDR: "::/0", Port: 443},
	} {
		p := newProviderV2("cidr-wildcard", mcpv1alpha2.MCPServerModeContainer)
		p.Spec.Image = "test@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
		p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
			Network: &mcpv1alpha2.NetworkCapabilitiesSpec{Egress: []mcpv1alpha2.EgressRuleSpec{rule}},
		}
		_, err := v.ValidateCreate(context.Background(), p)
		require.Error(t, err, rule.CIDR)
		assert.Contains(t, err.Error(), "unrestricted egress")

		p.Annotations = map[string]string{"hangar.io/allow-unrestricted-egress": "true"}
		_, err = v.ValidateCreate(context.Background(), p)
		assert.NoError(t, err, "%s with the annotation", rule.CIDR)
	}
}

func TestV2_NarrowCIDRIsNotUnrestricted(t *testing.T) {
	v := &webhook.MCPServerV1alpha2Validator{}
	p := newProviderV2("cidr-narrow", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "test@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "api.example.com", CIDR: "10.0.0.0/8", Port: 443}},
		},
	}
	_, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err)
}

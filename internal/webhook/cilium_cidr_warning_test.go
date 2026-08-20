package webhook_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// TestCiliumCIDRWarning covers #152: a CIDR egress rule naming an in-cluster
// upstream passes validation and is then silently dropped by stock Cilium,
// which matches policy on security identities rather than IPs. The warning must
// fire only where it is true -- on Cilium, for a range in-cluster IPs come from
// -- and stay quiet on any other CNI, which honours the same rule.
func TestCiliumCIDRWarning(t *testing.T) {
	tests := []struct {
		name        string
		cilium      bool
		cidr        string
		wantWarning bool
	}{
		{"cilium, in-cluster pod IP", true, "192.168.1.7/32", true},
		{"cilium, in-cluster pod CIDR", true, "10.244.0.0/16", true},
		{"cilium, external upstream", true, "203.0.113.0/24", false},
		{"calico, in-cluster pod IP", false, "192.168.1.7/32", false},
		{"calico, external upstream", false, "203.0.113.0/24", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhook.SetCiliumDetected(tt.cilium)
			t.Cleanup(func() { webhook.SetCiliumDetected(false) })

			p := newProviderV2("cidr-egress", mcpv1alpha2.MCPServerModeRemote)
			p.Spec.Endpoint = "https://upstream.example.com"
			p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
				Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
					Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "upstream", CIDR: tt.cidr, Port: 8080}},
				},
			}

			v := &webhook.MCPServerV1alpha2Validator{}
			warnings, err := v.ValidateCreate(context.Background(), p)
			require.NoError(t, err, "the rule must be admitted -- this is a warning, never a rejection")

			got := strings.Contains(strings.Join(warnings, "\n"), "policyCIDRMatchMode")
			assert.Equal(t, tt.wantWarning, got,
				"warnings=%v", warnings)
		})
	}
}

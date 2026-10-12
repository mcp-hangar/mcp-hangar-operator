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

// With --webhook-annotation-rules=false the ValidatingAdmissionPolicies own the
// wildcard egress opt-in and the image digest policy (#219); the webhook drops
// its copies of those two and keeps everything else, the Cilium CIDR warning
// among it.
func TestAnnotationRulesOff_LeaveThemToTheAdmissionPolicies(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("block"))
	webhook.SetAnnotationRules(false)
	webhook.SetCiliumDetected(true)
	t.Cleanup(func() {
		_ = webhook.SetImageDigestPolicy("warn")
		webhook.SetAnnotationRules(true)
		webhook.SetCiliumDetected(false)
	})

	p := wildcardProvider("rules-off")
	p.Spec.Capabilities.Network.Egress = append(p.Spec.Capabilities.Network.Egress,
		mcpv1alpha2.EgressRuleSpec{Host: "upstream", CIDR: "10.244.0.0/16", Port: 8080})

	v := &webhook.MCPServerV1alpha2Validator{}
	warnings, err := v.ValidateCreate(context.Background(), p)
	require.NoError(t, err, "a wildcard rule and an unpinned image are the policies' to refuse now")
	assert.Contains(t, strings.Join(warnings, "\n"), "policyCIDRMatchMode",
		"the Cilium CIDR warning stays in the webhook")

	webhook.SetAnnotationRules(true)
	_, err = v.ValidateCreate(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow-unrestricted-egress")
	assert.Contains(t, err.Error(), "is not digest-pinned")
}

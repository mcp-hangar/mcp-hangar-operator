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

// The ValidatingAdmissionPolicies own the wildcard egress opt-in and the image
// digest policy (#219); the webhook copies are gone (#312). The webhook admits
// both and keeps everything else, the Cilium CIDR warning among it. The rules
// themselves are tested against a real apiserver in internal/admissionpolicy.
func TestWebhook_LeavesTheAnnotationRulesToTheAdmissionPolicies(t *testing.T) {
	webhook.SetCiliumDetected(true)
	t.Cleanup(func() { webhook.SetCiliumDetected(false) })

	p := newProviderV2("rules-gone", mcpv1alpha2.MCPServerModeContainer)
	p.Spec.Image = "ghcr.io/test/provider:latest"
	p.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{
				{Host: "*", Port: 443, Protocol: "https"},
				{Host: "any", CIDR: "0.0.0.0/0", Port: 443},
				{Host: "upstream", CIDR: "10.244.0.0/16", Port: 8080},
			},
		},
	}

	v := &webhook.MCPServerV1alpha2Validator{}
	warnings, err := v.ValidateCreate(context.Background(), p)
	require.NoError(t, err, "a wildcard rule and an unpinned image are the policies' to refuse")
	joined := strings.Join(warnings, "\n")
	assert.NotContains(t, joined, "digest-pinned", "the digest warning is the warn policy's now")
	assert.Contains(t, joined, "policyCIDRMatchMode", "the Cilium CIDR warning stays in the webhook")

	_, err = v.ValidateUpdate(context.Background(), p, p)
	require.NoError(t, err)
}

// The controller's egress pin coupling reads IsImageDigestPinned (#52), and
// the image-digest policies mirror it.
func TestIsImageDigestPinned(t *testing.T) {
	const digest = "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	optOut := map[string]string{"hangar.io/allow-mutable-image": "true"}
	for _, tc := range []struct {
		image       string
		annotations map[string]string
		pinned      bool
	}{
		{"", nil, true},
		{"ghcr.io/org/app" + digest, nil, true},
		{"ghcr.io/org/app:v1" + digest, nil, true},
		{"ghcr.io/org/app:v1", nil, false},
		{"ghcr.io/org/app", nil, false},
		{"ghcr.io/org/app:v1", optOut, true},
		{"ghcr.io/org/app:v1", map[string]string{"hangar.io/allow-mutable-image": "yes"}, false},
	} {
		assert.Equal(t, tc.pinned, webhook.IsImageDigestPinned(tc.image, tc.annotations), "%q %v", tc.image, tc.annotations)
	}
}

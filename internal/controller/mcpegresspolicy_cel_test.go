package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// The Mcp-Param-* restriction is enforced by the API server, not by the
// controller and not by a Go mirror of the CEL (#160).
//
// These run against envtest loaded with the CRDs this repo actually ships, CEL
// rules and all -- the arrangement #111 put in place after the suite was found
// stripping x-kubernetes-validations, which is how #54 escaped. A test that
// re-implemented the rule in Go would pass while the shipped CRD accepted
// anything.

func policyWithHeaderDeny(name string, match mcpv1alpha2.HeaderMatch) *mcpv1alpha2.MCPEgressPolicy {
	p := testPolicy(name, "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
		Name:    "u",
		Match:   mcpv1alpha2.UpstreamMatch{Host: "a.com"},
		Headers: &mcpv1alpha2.HeaderRules{Deny: []mcpv1alpha2.HeaderMatch{match}},
	}}
	return p
}

func TestCEL_Envtest_AnMcpParamSelectorIsAccepted(t *testing.T) {
	p := policyWithHeaderDeny("cel-ok", mcpv1alpha2.HeaderMatch{
		Name: "Mcp-Param-Region", Values: []string{"us-*"},
	})

	require.NoError(t, k8sClient.Create(ctx, p))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })
}

func TestCEL_Envtest_TheHeaderNameIsMatchedCaseInsensitively(t *testing.T) {
	// HTTP header names are not case-sensitive, and an author writes them in
	// whatever case reads best.
	p := policyWithHeaderDeny("cel-case", mcpv1alpha2.HeaderMatch{
		Name: "MCP-PARAM-REGION", Values: []string{"us-*"},
	})

	require.NoError(t, k8sClient.Create(ctx, p))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, p) })
}

func TestCEL_Envtest_ACredentialHeaderIsRejected(t *testing.T) {
	// The reason the rule exists: a selector on Authorization would make an
	// egress policy a way to read a credential by writing globs until one
	// matched.
	p := policyWithHeaderDeny("cel-authz", mcpv1alpha2.HeaderMatch{
		Name: "Authorization", Values: []string{"Bearer *"},
	})

	err := k8sClient.Create(ctx, p)

	require.Error(t, err, "the API server must refuse this, not the controller")
	assert.Contains(t, strings.ToLower(err.Error()), "only mcp-param-* headers are selectable")
}

func TestCEL_Envtest_ASelectorWithNoValuesIsRejected(t *testing.T) {
	// A selector with nothing to match can never fire; accepting it would
	// report a control as configured while it is inert.
	p := policyWithHeaderDeny("cel-empty", mcpv1alpha2.HeaderMatch{
		Name: "Mcp-Param-Region", Values: []string{},
	})

	err := k8sClient.Create(ctx, p)

	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "values")
}

func TestCEL_Envtest_TheRuleAppliesToEveryBucket(t *testing.T) {
	// allow / deny / requireApproval share one item schema; a rule that landed
	// on only one of them would be a hole in the other two.
	for _, bucket := range []string{"allow", "requireApproval"} {
		t.Run(bucket, func(t *testing.T) {
			bad := []mcpv1alpha2.HeaderMatch{{Name: "Cookie", Values: []string{"*"}}}
			rules := &mcpv1alpha2.HeaderRules{}
			if bucket == "allow" {
				rules.Allow = bad
			} else {
				rules.RequireApproval = bad
			}
			p := testPolicy("cel-bucket-"+strings.ToLower(bucket), "default")
			p.ObjectMeta = metav1.ObjectMeta{Name: "cel-bucket-" + strings.ToLower(bucket), Namespace: "default"}
			p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{
				Name:    "u",
				Match:   mcpv1alpha2.UpstreamMatch{Host: "a.com"},
				Headers: rules,
			}}

			err := k8sClient.Create(ctx, p)

			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), "only mcp-param-* headers are selectable")
		})
	}
}

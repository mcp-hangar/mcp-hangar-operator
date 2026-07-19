package networkpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func egressPolicy(upstreamHosts ...string) *mcpv1alpha2.MCPEgressPolicy {
	p := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "ns"},
	}
	for i, h := range upstreamHosts {
		p.Spec.Upstreams = append(p.Spec.Upstreams, mcpv1alpha2.UpstreamRule{
			Name:  "u" + string(rune('a'+i)),
			Match: mcpv1alpha2.UpstreamMatch{Host: h},
		})
	}
	return p
}

func TestBuildEgressPolicyBackstop_CIDRAndFQDN(t *testing.T) {
	p := egressPolicy("10.0.0.0/8", "203.0.113.5", "api.example.com")
	np, unenforceable := BuildEgressPolicyBackstop(p, metav1.LabelSelector{
		MatchLabels: map[string]string{LabelProvider: "srv"},
	})

	assert.Equal(t, EgressPolicyBackstopName("pol"), np.Name)
	assert.Equal(t, "ns", np.Namespace)
	assert.Equal(t, "srv", np.Spec.PodSelector.MatchLabels[LabelProvider])

	// DNS + two CIDR rules (the /8 and the bare IP as /32); FQDN produces none.
	require.Len(t, np.Spec.Egress, 3)
	assert.Equal(t, []string{"api.example.com"}, unenforceable)

	// The bare IP became a /32 IPBlock.
	var cidrs []string
	for _, rule := range np.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				cidrs = append(cidrs, peer.IPBlock.CIDR)
			}
		}
	}
	assert.Contains(t, cidrs, "10.0.0.0/8")
	assert.Contains(t, cidrs, "203.0.113.5/32")
}

func TestBuildEgressPolicyCiliumNetworkPolicy_Shape(t *testing.T) {
	p := egressPolicy("api.github.com", "slack.com", "10.1.0.0/16")
	cnp := BuildEgressPolicyCiliumNetworkPolicy(p, "srv")

	assert.Equal(t, CiliumGroup+"/"+CiliumVersion, cnp.GetAPIVersion())
	assert.Equal(t, CiliumNetworkPolicyKind, cnp.GetKind())
	assert.Equal(t, EgressPolicyBackstopName("pol"), cnp.GetName())
	assert.Equal(t, "pol", cnp.GetLabels()[LabelEgressPolicy])

	// endpointSelector targets the provider.
	ep, found, err := unstructured.NestedString(cnp.Object, "spec", "endpointSelector", "matchLabels", LabelProvider)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "srv", ep)

	egress, found, err := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	require.NoError(t, err)
	require.True(t, found)

	var hasDNSRule, hasFQDNs, hasCIDR bool
	var fqdnNames []string
	for _, e := range egress {
		m := e.(map[string]interface{})
		if _, ok := m["toPorts"]; ok {
			// DNS rule must carry the L7 dns matchPattern so toFQDNs works.
			tp := m["toPorts"].([]interface{})[0].(map[string]interface{})
			if _, ok := tp["rules"]; ok {
				hasDNSRule = true
			}
		}
		if fq, ok := m["toFQDNs"]; ok {
			hasFQDNs = true
			for _, f := range fq.([]interface{}) {
				fqdnNames = append(fqdnNames, f.(map[string]interface{})["matchName"].(string))
			}
		}
		if _, ok := m["toCIDR"]; ok {
			hasCIDR = true
		}
	}
	assert.True(t, hasDNSRule, "DNS rule must include the L7 dns proxy rule")
	assert.True(t, hasFQDNs, "FQDN upstreams must produce toFQDNs")
	assert.True(t, hasCIDR, "CIDR upstream must produce toCIDR")
	assert.ElementsMatch(t, []string{"api.github.com", "slack.com"}, fqdnNames)
}

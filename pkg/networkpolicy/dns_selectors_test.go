package networkpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

const openshiftDNS = "openshift-dns/dns.operator.openshift.io/daemonset-dns=default"

func withDNSSelectors(t *testing.T, specs ...string) {
	t.Helper()
	require.NoError(t, SetExtraDNSSelectors(specs))
	t.Cleanup(func() { ExtraDNSSelectors = nil })
}

// On OpenShift the resolver is openshift-dns, not kube-system/kube-dns, so a
// policy that names only kube-dns cuts DNS off entirely (#203).
func assertOpenShiftDNSPeer(t *testing.T, rule networkingv1.NetworkPolicyEgressRule) {
	t.Helper()
	require.Len(t, rule.To, 2, "kube-dns plus the configured resolver")
	peer := rule.To[1]
	require.NotNil(t, peer.NamespaceSelector)
	require.NotNil(t, peer.PodSelector)
	assert.Equal(t, "openshift-dns", peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	assert.Equal(t, map[string]string{"dns.operator.openshift.io/daemonset-dns": "default"}, peer.PodSelector.MatchLabels)
}

func TestDNSSelectors_PerServerPolicy(t *testing.T) {
	withDNSSelectors(t, openshiftDNS)
	np := BuildNetworkPolicy(&mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "demo"},
		Spec: mcpv1alpha2.MCPServerSpec{Capabilities: &mcpv1alpha2.MCPServerCapabilities{
			Network: &mcpv1alpha2.NetworkCapabilitiesSpec{DNSAllowed: boolPtr(true)},
		}},
	})
	require.NotNil(t, np)
	assertOpenShiftDNSPeer(t, np.Spec.Egress[0])
}

func TestDNSSelectors_NamespaceDefaultDeny(t *testing.T) {
	withDNSSelectors(t, openshiftDNS)
	np := BuildNamespaceDefaultDenyEgress("demo")
	require.Len(t, np.Spec.Egress, 1)
	assertOpenShiftDNSPeer(t, np.Spec.Egress[0])
}

func TestDNSSelectors_EgressPolicyBackstop(t *testing.T) {
	withDNSSelectors(t, openshiftDNS)
	np, _ := BuildEgressPolicyBackstop(&mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "demo"},
	}, metav1.LabelSelector{MatchLabels: map[string]string{LabelProvider: "srv"}})
	require.NotEmpty(t, np.Spec.Egress)
	assertOpenShiftDNSPeer(t, np.Spec.Egress[0])
}

func TestDNSSelectors_CiliumBackstop(t *testing.T) {
	withDNSSelectors(t, openshiftDNS)
	cnp := BuildEgressPolicyCiliumNetworkPolicy(&mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "demo"},
	}, metav1.LabelSelector{MatchLabels: map[string]string{LabelProvider: "srv"}})

	egress := cnp.Object["spec"].(map[string]interface{})["egress"].([]interface{})
	endpoints := egress[0].(map[string]interface{})["toEndpoints"].([]interface{})
	require.Len(t, endpoints, 2)
	assert.Equal(t, map[string]interface{}{
		"k8s:io.kubernetes.pod.namespace":         "openshift-dns",
		"dns.operator.openshift.io/daemonset-dns": "default",
	}, endpoints[1].(map[string]interface{})["matchLabels"])
	// The DNS proxy rule still covers the extra resolver: toFQDNs needs it.
	assert.Contains(t, egress[0].(map[string]interface{}), "toPorts")
}

func TestDNSSelectors_DefaultIsKubeDNSOnly(t *testing.T) {
	np := BuildNamespaceDefaultDenyEgress("demo")
	assert.Len(t, np.Spec.Egress[0].To, 1)
}

func TestSetExtraDNSSelectors_Rejects(t *testing.T) {
	t.Cleanup(func() { ExtraDNSSelectors = nil })
	for _, spec := range []string{
		"openshift-dns",          // no labels
		"openshift-dns/",         // empty label set
		"/k8s-app=dns",           // no namespace
		"Not_A_Namespace/a=b",    // invalid namespace
		"openshift-dns/a=b=c",    // bad label syntax
		"openshift-dns/a in (b)", // set-based: not a plain label map
	} {
		assert.Error(t, SetExtraDNSSelectors([]string{spec}), spec)
	}
}

func TestSetExtraDNSSelectors_ParsesSeveral(t *testing.T) {
	withDNSSelectors(t, openshiftDNS, " dns/app=resolver,tier=edge ")
	require.Len(t, ExtraDNSSelectors, 2)
	assert.Equal(t, DNSSelector{Namespace: "dns", Labels: map[string]string{"app": "resolver", "tier": "edge"}},
		ExtraDNSSelectors[1])
}

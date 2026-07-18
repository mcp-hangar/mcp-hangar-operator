package networkpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
)

func TestBuildNamespaceDefaultDenyEgress(t *testing.T) {
	np := BuildNamespaceDefaultDenyEgress("demo")
	require.NotNil(t, np)

	assert.Equal(t, DefaultDenyEgressName, np.Name)
	assert.Equal(t, "demo", np.Namespace)
	assert.Equal(t, DefaultManagerName, np.Labels[LabelManagedBy])

	// All pods, egress-typed (so egress is default-deny except what's listed).
	assert.Empty(t, np.Spec.PodSelector.MatchLabels, "empty selector = all pods")
	assert.Empty(t, np.Spec.PodSelector.MatchExpressions)
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)

	// The only allowed egress is DNS, scoped to kube-dns.
	require.Len(t, np.Spec.Egress, 1, "only the DNS rule")
	dns := np.Spec.Egress[0]
	require.Len(t, dns.Ports, 2, "UDP + TCP 53")
	require.Len(t, dns.To, 1, "scoped to a peer, not open")
	peer := dns.To[0]
	require.NotNil(t, peer.NamespaceSelector)
	assert.Equal(t, "kube-system", peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	require.NotNil(t, peer.PodSelector)
	assert.Equal(t, "kube-dns", peer.PodSelector.MatchLabels["k8s-app"])
}

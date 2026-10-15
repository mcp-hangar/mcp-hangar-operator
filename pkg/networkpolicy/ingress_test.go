package networkpolicy

import (
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestProviderIngress_RequiresBothPeerSelectors(t *testing.T) {
	for _, tc := range []struct {
		enabled      bool
		ns, selector string
	}{
		{true, "", "app=gateway"}, {true, "gateway", ""}, {false, "gateway", ""}, {true, "bad/namespace", "app=gateway"}, {true, "gateway", "invalid==="},
	} {
		_, err := ParseProviderIngressConfig(tc.enabled, tc.ns, tc.selector)
		require.Error(t, err)
	}
	disabled, err := ParseProviderIngressConfig(false, "", "")
	require.NoError(t, err)
	server := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "providers"}}
	_, err = BuildProviderIngress(server, disabled)
	require.Error(t, err)
	config, err := ParseProviderIngressConfig(true, "gateway", "app in (gateway,frontdoor)")
	require.NoError(t, err)
	np, err := BuildProviderIngress(server, config)
	require.NoError(t, err)
	require.Len(t, np.Spec.Ingress, 1)
	require.Len(t, np.Spec.Ingress[0].From, 1)
	peer := np.Spec.Ingress[0].From[0]
	assert.Equal(t, "gateway", peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	require.Len(t, peer.PodSelector.MatchExpressions, 1)
	assert.Equal(t, "app", peer.PodSelector.MatchExpressions[0].Key)
	assert.Equal(t, int32(8080), np.Spec.Ingress[0].Ports[0].Port.IntVal)
	assert.Equal(t, "TCP", string(*np.Spec.Ingress[0].Ports[0].Protocol))
	assert.Equal(t, "server", np.Spec.PodSelector.MatchLabels[LabelProvider])
}

package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// type: Namespace yields one remote MCPServer per matching namespace, and a
// namespace names no Service, so the endpoint can only come from the template.
// Without one the source produced MCPServers the CEL rule on mode: remote then
// refused, one failed create per namespace per scan (#329).

func TestMCPDiscoverySource_NamespaceWithoutAnEndpointIsRefusedAtAdmission(t *testing.T) {
	ns := createNamespace(t, "test-disc-nsref")
	defer k8sClient.Delete(ctx, ns)

	src := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "nsref", Namespace: ns.Name},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:              mcpv1alpha2.DiscoveryTypeNamespace,
			NamespaceSelector: &mcpv1alpha2.NamespaceSelectorConfig{MatchLabels: map[string]string{"x": "y"}},
		},
	}

	err := k8sClient.Create(ctx, src)
	require.Error(t, err, "a Namespace source with no endpoint to give its servers must not be stored")
	require.Contains(t, err.Error(), "spec.providerTemplate.spec.endpoint is required when spec.type is Namespace")
}

func TestMCPDiscoverySource_NamespaceWithAnEndpointCreatesValidServers(t *testing.T) {
	ns := createNamespace(t, "test-disc-nsok")
	defer k8sClient.Delete(ctx, ns)

	label := "disc-" + rand.String(8)
	target := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "disc-target-" + rand.String(5),
		Labels: map[string]string{"mcp-hangar.io/test-discover": label},
	}}
	require.NoError(t, k8sClient.Create(ctx, target))
	defer k8sClient.Delete(ctx, target)

	const endpoint = "http://gateway.example.svc.cluster.local:8080/mcp"
	src := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "nsok", Namespace: ns.Name},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:              mcpv1alpha2.DiscoveryTypeNamespace,
			Mode:              mcpv1alpha2.DiscoveryModeAdditive,
			NamespaceSelector: &mcpv1alpha2.NamespaceSelectorConfig{MatchLabels: map[string]string{"mcp-hangar.io/test-discover": label}},
			MCPServerTemplate: &mcpv1alpha2.MCPServerTemplateConfig{
				Spec: &mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: endpoint},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, src))

	waitForManagedProviderCount(t, "nsok", ns.Name, 1)
	servers := &mcpv1alpha2.MCPServerList{}
	require.NoError(t, k8sClient.List(ctx, servers, client.InNamespace(ns.Name),
		client.MatchingLabels{LabelDiscoveryManagedBy: "nsok"}))
	require.Len(t, servers.Items, 1)
	require.Equal(t, mcpv1alpha2.MCPServerModeRemote, servers.Items[0].Spec.Mode)
	require.Equal(t, endpoint, servers.Items[0].Spec.Endpoint)
}

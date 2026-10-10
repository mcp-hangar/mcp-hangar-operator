package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func TestValidateDiscoveredName(t *testing.T) {
	assert.NoError(t, validateDiscoveredName("src-provider-a"))
	for _, bad := range []string{"src-Provider_A", "src-a/b", "src-x0123456789012345678901234567890123456789012345678901234567890"} {
		assert.Error(t, validateDiscoveredName(bad), bad)
	}
}

// A ConfigMap key Kubernetes cannot use as a name used to reach Create and
// fail there; it is now refused per entry with the reason in status, and the
// valid entries are still managed (#213).
func TestMCPDiscoverySource_InvalidEntryNameIsReportedNotCreated(t *testing.T) {
	ns := createNamespace(t, "test-disc-badname")
	defer k8sClient.Delete(ctx, ns)

	createConfigMap(t, "badname-cm", ns.Name, `good:
  mode: remote
  endpoint: http://good:8080
Bad_Name:
  mode: remote
  endpoint: http://bad:8080
`)
	createDiscoverySource(t, "bn", ns.Name, "badname-cm", mcpv1alpha2.DiscoveryModeAdditive)

	waitForManagedProviderCount(t, "bn", ns.Name, 1)
	require.Eventually(t, func() bool {
		src := &mcpv1alpha2.MCPDiscoverySource{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "bn", Namespace: ns.Name}, src); err != nil {
			return false
		}
		for _, p := range src.Status.DiscoveredMCPServers {
			if p.Name == "bn-Bad_Name" && !p.Managed && strings.Contains(p.Error, "invalid MCPServer name") {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "the invalid entry should be refused by name validation, before any Create")
}

// An Additive source that does not own its servers adds them and leaves them
// alone; deleting it used to delete them anyway (#213).
func TestMCPDiscoverySource_AdditiveUnownedDeletionKeepsServers(t *testing.T) {
	ns := createNamespace(t, "test-disc-keep")
	defer k8sClient.Delete(ctx, ns)

	createConfigMap(t, "keep-cm", ns.Name, twoProviderYAML)
	source := &mcpv1alpha2.MCPDiscoverySource{}
	source.Name, source.Namespace = "keep", ns.Name
	source.Spec = mcpv1alpha2.MCPDiscoverySourceSpec{
		Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
		Mode:         mcpv1alpha2.DiscoveryModeAdditive,
		ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "keep-cm"},
		Ownership:    &mcpv1alpha2.OwnershipConfig{Controller: ptr.To(false)},
	}
	require.NoError(t, k8sClient.Create(ctx, source))
	waitForManagedProviderCount(t, "keep", ns.Name, 2)

	require.NoError(t, k8sClient.Delete(ctx, source))
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, types.NamespacedName{Name: "keep", Namespace: ns.Name}, &mcpv1alpha2.MCPDiscoverySource{}) != nil
	}, 15*time.Second, 250*time.Millisecond, "source should be gone")

	list := &mcpv1alpha2.MCPServerList{}
	require.NoError(t, k8sClient.List(ctx, list, client.InNamespace(ns.Name),
		client.MatchingLabels{LabelDiscoveryManagedBy: "keep"}))
	assert.Len(t, list.Items, 2, "an Additive, unowned source's servers outlive it")
}

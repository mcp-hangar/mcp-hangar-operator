package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
)

// waitForGroupCondition polls until the specified condition reaches the expected status
func waitForGroupCondition(t *testing.T, name, namespace, condType string, status metav1.ConditionStatus) {
	t.Helper()
	require.Eventually(t, func() bool {
		group := &mcpv1alpha1.MCPServerGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, group); err != nil {
			return false
		}
		for _, c := range group.Status.Conditions {
			if c.Type == condType && c.Status == status {
				return true
			}
		}
		return false
	}, 10*time.Second, 250*time.Millisecond, "condition %s=%s not met for group %s/%s", condType, status, namespace, name)
}

// waitForGroupMCPServerCount polls until the group status shows the expected provider count
func waitForGroupMCPServerCount(t *testing.T, name, namespace string, count int32) {
	t.Helper()
	require.Eventually(t, func() bool {
		group := &mcpv1alpha1.MCPServerGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, group); err != nil {
			return false
		}
		return group.Status.ProviderCount == count
	}, 10*time.Second, 250*time.Millisecond, "expected provider count %d for group %s/%s", count, namespace, name)
}

// createMCPServer creates an MCPServer and sets its status state via the status subresource.
// Note: MCPServer reconciler runs concurrently and may temporarily override the state.
// The group reconciler reads the state at reconcile time, so we retry setting state
// and give the group reconciler time to pick up the desired state.
func createMCPServer(t *testing.T, name, namespace string, state mcpv1alpha1.MCPServerState, labels map[string]string) *mcpv1alpha1.MCPServer {
	t.Helper()
	provider := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode: mcpv1alpha1.MCPServerModeRemote,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, provider))

	// Update status subresource to set state (retry on conflict)
	require.Eventually(t, func() bool {
		p := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, p); err != nil {
			return false
		}
		p.Status.State = state
		return k8sClient.Status().Update(ctx, p) == nil
	}, 10*time.Second, 100*time.Millisecond, "failed to set provider %s state to %s", name, state)

	return provider
}

// createNamespace creates a namespace for test isolation
func createNamespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	require.NoError(t, k8sClient.Create(ctx, ns))
	return ns
}

func TestMCPServerGroup_LabelSelection(t *testing.T) {
	ns := createNamespace(t, "test-group-label-sel")
	defer k8sClient.Delete(ctx, ns)

	// Create group selecting app=web
	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "label-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	webLabels := map[string]string{"app": "web"}
	createMCPServer(t, "web-ready", ns.Name, mcpv1alpha1.MCPServerStateReady, webLabels)
	createMCPServer(t, "web-degraded", ns.Name, mcpv1alpha1.MCPServerStateDegraded, webLabels)
	// This one should NOT be selected
	createMCPServer(t, "api-ready", ns.Name, mcpv1alpha1.MCPServerStateReady, map[string]string{"app": "api"})

	// Wait for group to reconcile with 2 providers
	waitForGroupMCPServerCount(t, "label-group", ns.Name, 2)

	// Verify counts
	result := &mcpv1alpha1.MCPServerGroup{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "label-group", Namespace: ns.Name}, result))
	assert.Equal(t, int32(2), result.Status.ProviderCount)
	assert.Equal(t, int32(1), result.Status.ReadyCount)
	assert.Equal(t, int32(1), result.Status.DegradedCount)

	// Verify unmatched provider is not in the list
	for _, p := range result.Status.Providers {
		assert.NotEqual(t, "api-ready", p.Name, "unmatched provider should not be in group")
	}
}

func TestMCPServerGroup_StatusAggregation(t *testing.T) {
	ns := createNamespace(t, "test-group-status-agg")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"tier": "backend"}

	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agg-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// Create providers in various states
	createMCPServer(t, "ready-1", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "ready-2", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "degraded-1", ns.Name, mcpv1alpha1.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "dead-1", ns.Name, mcpv1alpha1.MCPServerStateDead, groupLabels)
	createMCPServer(t, "cold-1", ns.Name, mcpv1alpha1.MCPServerStateCold, groupLabels)

	waitForGroupMCPServerCount(t, "agg-group", ns.Name, 5)

	result := &mcpv1alpha1.MCPServerGroup{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "agg-group", Namespace: ns.Name}, result))

	assert.Equal(t, int32(5), result.Status.ProviderCount)
	assert.Equal(t, int32(2), result.Status.ReadyCount)
	assert.Equal(t, int32(1), result.Status.DegradedCount)
	assert.Equal(t, int32(1), result.Status.DeadCount)
	assert.Equal(t, int32(1), result.Status.ColdCount)
	assert.Len(t, result.Status.Providers, 5)
}

func TestMCPServerGroup_HealthPolicyThreshold(t *testing.T) {
	ns := createNamespace(t, "test-group-health-thresh")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"pool": "threshold"}
	minPct := int32(60)

	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "threshold-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
			HealthPolicy: &mcpv1alpha1.HealthPolicy{
				MinHealthyPercentage: minPct,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// 3 ready + 2 dead = 60% healthy (meets threshold exactly)
	createMCPServer(t, "h-ready-1", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-ready-2", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-ready-3", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-dead-1", ns.Name, mcpv1alpha1.MCPServerStateDead, groupLabels)
	createMCPServer(t, "h-dead-2", ns.Name, mcpv1alpha1.MCPServerStateDead, groupLabels)

	// Threshold met at exactly 60%
	waitForGroupCondition(t, "threshold-group", ns.Name, ConditionReady, metav1.ConditionTrue)
	// Dead providers exist so Degraded is True
	waitForGroupCondition(t, "threshold-group", ns.Name, ConditionDegraded, metav1.ConditionTrue)
	// At least 1 ready so Available is True
	waitForGroupCondition(t, "threshold-group", ns.Name, ConditionAvailable, metav1.ConditionTrue)
}

func TestMCPServerGroup_ZeroMembers(t *testing.T) {
	ns := createNamespace(t, "test-group-zero-members")
	defer k8sClient.Delete(ctx, ns)

	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"nonexistent": "label"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// No providers match, so Ready=Unknown with reason NoProviders
	waitForGroupCondition(t, "empty-group", ns.Name, ConditionReady, metav1.ConditionUnknown)
	waitForGroupCondition(t, "empty-group", ns.Name, ConditionAvailable, metav1.ConditionFalse)
	waitForGroupCondition(t, "empty-group", ns.Name, ConditionDegraded, metav1.ConditionFalse)

	// Verify reason
	result := &mcpv1alpha1.MCPServerGroup{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "empty-group", Namespace: ns.Name}, result))
	for _, c := range result.Status.Conditions {
		if c.Type == ConditionReady {
			assert.Equal(t, "NoProviders", c.Reason)
			break
		}
	}
}

func TestMCPServerGroup_CoexistingReadyDegraded(t *testing.T) {
	ns := createNamespace(t, "test-group-coexist")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"pool": "coexist"}

	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "coexist-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
			HealthPolicy: &mcpv1alpha1.HealthPolicy{
				MinHealthyPercentage: 30,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// 2 ready + 3 degraded = 40% healthy (above 30% threshold)
	createMCPServer(t, "co-ready-1", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "co-ready-2", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "co-deg-1", ns.Name, mcpv1alpha1.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "co-deg-2", ns.Name, mcpv1alpha1.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "co-deg-3", ns.Name, mcpv1alpha1.MCPServerStateDegraded, groupLabels)

	// Both Ready=True and Degraded=True simultaneously
	waitForGroupCondition(t, "coexist-group", ns.Name, ConditionReady, metav1.ConditionTrue)
	waitForGroupCondition(t, "coexist-group", ns.Name, ConditionDegraded, metav1.ConditionTrue)
}

func TestMCPServerGroup_Deletion(t *testing.T) {
	ns := createNamespace(t, "test-group-deletion")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"pool": "deleteme"}

	group := &mcpv1alpha1.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "del-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha1.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	createMCPServer(t, "del-ready-1", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)
	createMCPServer(t, "del-ready-2", ns.Name, mcpv1alpha1.MCPServerStateReady, groupLabels)

	// Wait for group to reconcile
	waitForGroupMCPServerCount(t, "del-group", ns.Name, 2)

	// Delete the group
	require.NoError(t, k8sClient.Delete(ctx, group))

	// Wait for group to be fully removed (finalizer cleaned up)
	require.Eventually(t, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "del-group", Namespace: ns.Name}, &mcpv1alpha1.MCPServerGroup{})
		return err != nil // NotFound expected
	}, 10*time.Second, 250*time.Millisecond, "group should be deleted")

	// Providers should still exist (group does not own providers)
	provider1 := &mcpv1alpha1.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "del-ready-1", Namespace: ns.Name}, provider1))
	provider2 := &mcpv1alpha1.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "del-ready-2", Namespace: ns.Name}, provider2))
}

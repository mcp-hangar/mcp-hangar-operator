package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/metrics"
)

// waitForGroupCondition polls until the specified condition reaches the expected status
func waitForGroupCondition(t *testing.T, name, namespace, condType string, status metav1.ConditionStatus) {
	t.Helper()
	require.Eventually(t, func() bool {
		group := &mcpv1alpha2.MCPServerGroup{}
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

// waitForGroupMCPServerCount polls until the group status shows the expected provider count.
//
// Waiting on the total is only safe when the total is all you then assert. A
// member joins the group in two writes -- Create, then a status update carrying
// its state -- and the group reconciles on both. So the first reconcile that
// sees the last member counts it with an empty state: the total is already
// right while the per-state counters are not. A test that waits here and then
// asserts ReadyCount passes or fails on which write the reconcile happened to
// observe, which is what made TestMCPServerGroup_StatusAggregation flake.
//
// Use waitForGroupCounts when the per-state counters matter.
func waitForGroupMCPServerCount(t *testing.T, name, namespace string, count int32) {
	t.Helper()
	require.Eventually(t, func() bool {
		group := &mcpv1alpha2.MCPServerGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, group); err != nil {
			return false
		}
		return group.Status.ProviderCount == count
	}, 10*time.Second, 250*time.Millisecond, "expected provider count %d for group %s/%s", count, namespace, name)
}

// groupCounts is the aggregate a group test actually cares about.
type groupCounts struct {
	Provider int32
	Ready    int32
	Degraded int32
	Dead     int32
	Cold     int32
}

func countsOf(group *mcpv1alpha2.MCPServerGroup) groupCounts {
	return groupCounts{
		Provider: group.Status.ProviderCount,
		Ready:    group.Status.ReadyCount,
		Degraded: group.Status.DegradedCount,
		Dead:     group.Status.DeadCount,
		Cold:     group.Status.ColdCount,
	}
}

// waitForGroupCounts polls until the whole aggregate matches, and returns the
// group it settled on.
//
// The difference from waitForGroupMCPServerCount is the point: the condition
// and the assertion are the same thing here, so there is no window between them
// for another reconcile to change the answer.
//
// The fresh object per poll is not tidiness, it is correctness, and it cost an
// afternoon to learn: every counter is `omitempty`, so a zero is absent from the
// response body, and decoding into a reused struct leaves the previous poll's
// value sitting there. A helper that reused one object reported counters that
// could only ever rise -- which reads exactly like a controller bug, and is not.
func waitForGroupCounts(t *testing.T, name, namespace string, want groupCounts) *mcpv1alpha2.MCPServerGroup {
	t.Helper()
	var settled *mcpv1alpha2.MCPServerGroup
	var last groupCounts
	var lastMembers []mcpv1alpha2.MCPServerMemberStatus
	require.Eventually(t, func() bool {
		group := &mcpv1alpha2.MCPServerGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, group); err != nil {
			return false
		}
		last = countsOf(group)
		lastMembers = group.Status.Providers
		if last != want {
			return false
		}
		settled = group
		return true
	}, 10*time.Second, 250*time.Millisecond,
		"group %s/%s never reached %+v; last seen %+v; members %+v", namespace, name, want, &last, lastMembers)
	return settled
}

// createMCPServer creates an MCPServer and sets its status state via the status subresource.
//
// This takes two writes, and that is the thing to keep in mind when using it:
// between the Create and the status update the member exists with an empty
// state, and the group reconciles on both events. Wait on what you assert.
//
// The MCPServer reconciler does NOT run in this suite -- `enableMCPServerReconciler`
// is false precisely so group tests can own these states. The note that used to
// be here said the opposite, and cost an investigation before the real race was
// found; the retry below is for ordinary optimistic-lock conflicts, not for a
// second controller fighting over the field.
func createMCPServer(t *testing.T, name, namespace string, state mcpv1alpha2.MCPServerState, labels map[string]string) *mcpv1alpha2.MCPServer {
	t.Helper()
	provider := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode: mcpv1alpha2.MCPServerModeRemote,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, provider))

	// Update status subresource to set state (retry on conflict)
	require.Eventually(t, func() bool {
		p := &mcpv1alpha2.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, p); err != nil {
			return false
		}
		p.Status.State = state
		return k8sClient.Status().Update(ctx, p) == nil
	}, 10*time.Second, 100*time.Millisecond, "failed to set provider %s state to %s", name, state)

	return provider
}

// createNamespace makes a namespace for test isolation, with a unique suffix.
//
// The suffix is what makes `-count=N` possible. envtest runs no namespace
// controller, so a deleted namespace stays Terminating forever and a fixed name
// fails the second run with "object is being deleted ... already exists". That
// turned the one tool for confirming a flake fix -- run it a hundred times --
// into a guaranteed failure, so nobody could tell a real fix from a lucky one.
func createNamespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s", name, rand.String(5))},
	}
	require.NoError(t, k8sClient.Create(ctx, ns))
	return ns
}

// A counter has to be able to come back down, and nothing pinned that.
//
// Every member starts with no state, which the aggregation counts as cold, so
// every group passes through a non-zero coldCount on its way to steady state.
// The existing tests only ever asserted counters going up, which leaves the
// falling direction untested for a status written as a diff patch -- and the
// falling direction is the one that decides whether a recovered group ever
// stops reporting degraded members.
//
// The controller gets this right today. This is here so a change to how the
// status is written cannot quietly lose it.
func TestMCPServerGroup_CountersReturnToZero(t *testing.T) {
	ns := createNamespace(t, "test-group-counter-zero")
	defer k8sClient.Delete(ctx, ns)

	labels := map[string]string{"tier": "zeroing"}
	require.NoError(t, k8sClient.Create(ctx, &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "zero-group", Namespace: ns.Name},
		Spec:       mcpv1alpha2.MCPServerGroupSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}},
	}))

	// Cold first, so the counter is genuinely non-zero before we ask it to fall.
	member := createMCPServer(t, "settles", ns.Name, mcpv1alpha2.MCPServerStateCold, labels)
	waitForGroupCounts(t, "zero-group", ns.Name, groupCounts{Provider: 1, Cold: 1})

	require.Eventually(t, func() bool {
		p := &mcpv1alpha2.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: member.Name, Namespace: ns.Name}, p); err != nil {
			return false
		}
		p.Status.State = mcpv1alpha2.MCPServerStateReady
		return k8sClient.Status().Update(ctx, p) == nil
	}, 10*time.Second, 100*time.Millisecond, "failed to move the member to Ready")

	// The assertion: coldCount is 0 again, not stuck at 1.
	waitForGroupCounts(t, "zero-group", ns.Name, groupCounts{Provider: 1, Ready: 1, Cold: 0})
}

func TestMCPServerGroup_LabelSelection(t *testing.T) {
	ns := createNamespace(t, "test-group-label-sel")
	defer k8sClient.Delete(ctx, ns)

	// Create group selecting app=web
	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "label-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	webLabels := map[string]string{"app": "web"}
	createMCPServer(t, "web-ready", ns.Name, mcpv1alpha2.MCPServerStateReady, webLabels)
	createMCPServer(t, "web-degraded", ns.Name, mcpv1alpha2.MCPServerStateDegraded, webLabels)
	// This one should NOT be selected
	createMCPServer(t, "api-ready", ns.Name, mcpv1alpha2.MCPServerStateReady, map[string]string{"app": "api"})

	// Wait for group to reconcile with 2 providers
	// Same latent race as the aggregation test: the selector test asserts
	// per-state counters, so it has to wait on them rather than on the total.
	result := waitForGroupCounts(t, "label-group", ns.Name, groupCounts{
		Provider: 2, Ready: 1, Degraded: 1,
	})

	// Verify unmatched provider is not in the list
	for _, p := range result.Status.Providers {
		assert.NotEqual(t, "api-ready", p.Name, "unmatched provider should not be in group")
	}
}

func TestMCPServerGroup_StatusAggregation(t *testing.T) {
	ns := createNamespace(t, "test-group-status-agg")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"tier": "backend"}

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agg-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// Create providers in various states
	createMCPServer(t, "ready-1", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "ready-2", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "degraded-1", ns.Name, mcpv1alpha2.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "dead-1", ns.Name, mcpv1alpha2.MCPServerStateDead, groupLabels)
	createMCPServer(t, "cold-1", ns.Name, mcpv1alpha2.MCPServerStateCold, groupLabels)

	result := waitForGroupCounts(t, "agg-group", ns.Name, groupCounts{
		Provider: 5, Ready: 2, Degraded: 1, Dead: 1, Cold: 1,
	})

	assert.Len(t, result.Status.Providers, 5)
}

func TestMCPServerGroup_HealthPolicyThreshold(t *testing.T) {
	ns := createNamespace(t, "test-group-health-thresh")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"pool": "threshold"}
	minPct := int32(60)

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "threshold-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
			HealthPolicy: &mcpv1alpha2.HealthPolicy{
				MinHealthyPercentage: minPct,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// 3 ready + 2 dead = 60% healthy (meets threshold exactly)
	createMCPServer(t, "h-ready-1", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-ready-2", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-ready-3", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "h-dead-1", ns.Name, mcpv1alpha2.MCPServerStateDead, groupLabels)
	createMCPServer(t, "h-dead-2", ns.Name, mcpv1alpha2.MCPServerStateDead, groupLabels)

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

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
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
	result := &mcpv1alpha2.MCPServerGroup{}
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

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "coexist-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
			HealthPolicy: &mcpv1alpha2.HealthPolicy{
				MinHealthyPercentage: 30,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// 2 ready + 3 degraded = 40% healthy (above 30% threshold)
	createMCPServer(t, "co-ready-1", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "co-ready-2", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "co-deg-1", ns.Name, mcpv1alpha2.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "co-deg-2", ns.Name, mcpv1alpha2.MCPServerStateDegraded, groupLabels)
	createMCPServer(t, "co-deg-3", ns.Name, mcpv1alpha2.MCPServerStateDegraded, groupLabels)

	// Both Ready=True and Degraded=True simultaneously
	waitForGroupCondition(t, "coexist-group", ns.Name, ConditionReady, metav1.ConditionTrue)
	waitForGroupCondition(t, "coexist-group", ns.Name, ConditionDegraded, metav1.ConditionTrue)
}

func TestMCPServerGroup_Deletion(t *testing.T) {
	ns := createNamespace(t, "test-group-deletion")
	defer k8sClient.Delete(ctx, ns)

	groupLabels := map[string]string{"pool": "deleteme"}

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "del-group",
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	createMCPServer(t, "del-ready-1", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	createMCPServer(t, "del-ready-2", ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)

	// Wait for group to reconcile
	waitForGroupMCPServerCount(t, "del-group", ns.Name, 2)

	// Delete the group
	require.NoError(t, k8sClient.Delete(ctx, group))

	// Wait for group to be fully removed (finalizer cleaned up)
	require.Eventually(t, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "del-group", Namespace: ns.Name}, &mcpv1alpha2.MCPServerGroup{})
		return err != nil // NotFound expected
	}, 10*time.Second, 250*time.Millisecond, "group should be deleted")

	// Providers should still exist (group does not own providers)
	provider1 := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "del-ready-1", Namespace: ns.Name}, provider1))
	provider2 := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "del-ready-2", Namespace: ns.Name}, provider2))
}

// groupStatusWriteCount reads the current total of MCPServerGroup status
// subresource write attempts (success + conflict) recorded for the given
// group, i.e. how many times the reconciler actually issued a
// Status().Update() call. It intentionally excludes "skipped" (no-op)
// writes and, once summed with the "error" result, gives the full attempt
// count.
func groupStatusWriteCount(namespace, name string) float64 {
	return testutil.ToFloat64(metrics.GroupStatusWriteTotal.WithLabelValues(namespace, name, "success")) +
		testutil.ToFloat64(metrics.GroupStatusWriteTotal.WithLabelValues(namespace, name, "conflict")) +
		testutil.ToFloat64(metrics.GroupStatusWriteTotal.WithLabelValues(namespace, name, "error"))
}

// TestMCPServerGroup_StatusWriteStormBounded reproduces the scale scenario
// from #32 (a Group with 30 members, created in a burst) and asserts the
// conflict-storm characteristic doesn't happen:
//   - the number of MCPServerGroup status write attempts stays bounded
//     relative to member count instead of growing into the thousands, and
//   - once the group has converged, writes stop -- there is no
//     self-sustaining churn (the storm in #32 kept growing even after
//     member creation quieted down, and only stopped when the Group was
//     deleted).
func TestMCPServerGroup_StatusWriteStormBounded(t *testing.T) {
	ns := createNamespace(t, "test-group-write-storm")
	defer k8sClient.Delete(ctx, ns)

	const memberCount = 30
	groupName := "storm-group"
	groupLabels := map[string]string{"pool": "storm"}

	group := &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupName,
			Namespace: ns.Name,
		},
		Spec: mcpv1alpha2.MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: groupLabels,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, group))

	// Burst-create members, mirroring the "30 MCPServer CRs in one apply"
	// scale scenario from #32. Each create (and each status-subresource
	// update inside createMCPServer) maps to a Group reconcile via
	// findGroupsForMCPServer.
	for i := 0; i < memberCount; i++ {
		createMCPServer(t, fmt.Sprintf("storm-member-%d", i), ns.Name, mcpv1alpha2.MCPServerStateReady, groupLabels)
	}

	waitForGroupMCPServerCount(t, groupName, ns.Name, memberCount)

	// Give the controller a settle window past convergence.
	time.Sleep(3 * time.Second)
	writesAfterSettle := groupStatusWriteCount(ns.Name, groupName)

	// #32's storm was still growing 50s after creation and only stopped when
	// the Group was deleted -- i.e. it never settles on its own. A second
	// quiet window with no further member churn must show zero additional
	// writes if the self-triggering loop is actually fixed.
	time.Sleep(3 * time.Second)
	writesAfterQuiet := groupStatusWriteCount(ns.Name, groupName)

	assert.Equal(t, writesAfterSettle, writesAfterQuiet,
		"group status writes must stop once converged; continued growth here is the #32 self-sustaining storm signature")

	// Bounded relative to member count, not proportional to the (much
	// larger) volume of reconcile triggers fanned in from member events.
	// #32 measured 1022+ conflict errors alone for 30 members; this asserts
	// we stay in the same order of magnitude as N, not two orders above it.
	assert.Less(t, writesAfterQuiet, float64(memberCount*3),
		"status write attempts should be bounded relative to member count, not the #32 storm's ~34x-per-member conflict rate")

	errorCount := testutil.ToFloat64(metrics.GroupStatusWriteTotal.WithLabelValues(ns.Name, groupName, "error"))
	assert.Zero(t, errorCount, "no reconcile errors expected from status writes at this scale with the fix applied")
}

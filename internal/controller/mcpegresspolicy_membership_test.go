package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// Pure mapping, against the fake client: which policies a server or group
// event reaches, and which it does not.
func TestPoliciesForMCPServer_MapsByNameAndGroupSelector(t *testing.T) {
	gold := map[string]string{"tier": "gold"}
	byName := testPolicy("by-name", "ns")
	byGroup := testPolicy("by-group", "ns")
	byGroup.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	otherGroup := testPolicy("other-group", "ns")
	otherGroup.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "silver"}
	otherServer := testPolicy("other-server", "ns")
	otherServer.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: "someone-else"}
	otherNS := testPolicy("other-ns", "elsewhere")
	r := newEgressReconciler(
		testGroup("grp", "ns", gold), testGroup("silver", "ns", map[string]string{"tier": "silver"}),
		byName, byGroup, otherGroup, otherServer, otherNS,
	)

	reqs := r.policiesForMCPServer(context.Background(), labeledServer("srv", "ns", gold))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: "by-name", Namespace: "ns"}},
		{NamespacedName: types.NamespacedName{Name: "by-group", Namespace: "ns"}},
	}, reqs)

	// Unlabelled: only the policy that names it.
	reqs = r.policiesForMCPServer(context.Background(), testServer("srv", "ns"))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: "by-name", Namespace: "ns"}},
	}, reqs)

	// A group that no longer exists maps to nothing; the policy is on its
	// TargetNotFound requeue already.
	missing := testPolicy("missing-group", "ns")
	missing.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "gone"}
	r = newEgressReconciler(missing)
	assert.Empty(t, r.policiesForMCPServer(context.Background(), labeledServer("srv", "ns", gold)))
}

func TestPoliciesForMCPServerGroup_MapsPoliciesTargetingTheGroup(t *testing.T) {
	byGroup := testPolicy("by-group", "ns")
	byGroup.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	sameNameServer := testPolicy("same-name-server", "ns")
	sameNameServer.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: "grp"}
	otherGroup := testPolicy("other-group", "ns")
	otherGroup.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "silver"}
	r := newEgressReconciler(byGroup, sameNameServer, otherGroup)

	reqs := r.policiesForMCPServerGroup(context.Background(), testGroup("grp", "ns", nil))
	assert.Equal(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: "by-group", Namespace: "ns"}},
	}, reqs)
}

// l7PushLog is a stand-in core that records L7 policy pushes per server.
type l7PushLog struct {
	mu     sync.Mutex
	pushes map[string]int
}

func newL7PushLog(t *testing.T) (*l7PushLog, *httptest.Server) {
	t.Helper()
	l := &l7PushLog{pushes: map[string]int{}}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// POST /api/mcp_servers/<name>/l7_policy
		if r.Method == http.MethodPost {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) == 4 && parts[1] == "mcp_servers" && parts[3] == "l7_policy" {
				l.mu.Lock()
				l.pushes[parts[2]]++
				l.mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"l7_policy_set":true,"persisted":false}`))
	}))
	t.Cleanup(core.Close)
	return l, core
}

func (l *l7PushLog) get(server string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pushes[server]
}

// waitFor polls until cond holds or limit passes, and reports which.
func waitFor(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

// waitForPush waits for the push count of server to exceed its previous value.
func (l *l7PushLog) waitForPush(server string, previous int, limit time.Duration) bool {
	return waitFor(limit, func() bool { return l.get(server) > previous })
}

// waitQuiet waits until no server's push count has moved for quiet.
func (l *l7PushLog) waitQuiet(t *testing.T, quiet, limit time.Duration) {
	t.Helper()
	snapshot := func() map[string]int {
		l.mu.Lock()
		defer l.mu.Unlock()
		out := make(map[string]int, len(l.pushes))
		for k, v := range l.pushes {
			out[k] = v
		}
		return out
	}
	deadline := time.Now().Add(limit)
	last, since := snapshot(), time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		cur := snapshot()
		if !assert.ObjectsAreEqual(last, cur) {
			last, since = cur, time.Now()
			continue
		}
		if time.Since(since) >= quiet {
			return
		}
	}
	t.Fatalf("pushes never settled (last %v)", last)
}

// backstopProviders returns the provider names the policy's backstop
// NetworkPolicy selects, or nil when it does not exist.
func backstopProviders(t *testing.T, policyName, ns string) []string {
	t.Helper()
	np := &networkingv1.NetworkPolicy{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: networkpolicy.EgressPolicyBackstopName(policyName), Namespace: ns}, np)
	if apierrors.IsNotFound(err) {
		return nil
	}
	require.NoError(t, err)
	return providerNamesFromSelector(np.Spec.PodSelector)
}

// startEgressManager runs the egress policy controller under a manager of its
// own, wired to core. The suite's shared manager does not register this
// controller, and the watches under test are the manager's.
func startEgressManager(t *testing.T, coreURL string) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, (&MCPEgressPolicyReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorder("mcpegresspolicy-controller"),
		HangarClient: hangar.NewClient(&hangar.Config{URL: coreURL, MaxRetries: 0}),
	}).SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() { _ = mgr.Start(mgrCtx) }()
}

func createPolicyWithCleanup(t *testing.T, policy *mcpv1alpha2.MCPEgressPolicy) {
	t.Helper()
	require.NoError(t, k8sClient.Create(ctx, policy))
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPEgressPolicy{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), cur); err == nil {
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		}
	})
}

func ensureNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

// The #190 shape end to end, against a real apiserver and a running
// controller: a policy targets a group, nothing about the policy changes, and
// the group's membership does. The backstop and the L7 push must follow each
// change in seconds, not at the informer resync.
func TestEgressPolicy_GroupMembershipChange_FollowsMembers(t *testing.T) {
	const nsName = "egress-membership"
	ensureNamespace(t, nsName)
	gold := map[string]string{"tier": "gold"}
	const deadline = 10 * time.Second

	pushes, core := newL7PushLog(t)
	startEgressManager(t, core.URL)

	require.NoError(t, k8sClient.Create(ctx, testGroup("grp", nsName, gold)))
	require.NoError(t, k8sClient.Create(ctx, labeledServer("m1", nsName, gold)))
	policy := testPolicy("pol", nsName)
	policy.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	createPolicyWithCleanup(t, policy)

	// Compiled for the one member, then quiet.
	require.True(t, pushes.waitForPush("m1", 0, 30*time.Second), "the policy was never delivered for m1")
	pushes.waitQuiet(t, 2*time.Second, 30*time.Second)
	require.Equal(t, []string{"m1"}, backstopProviders(t, "pol", nsName))
	cur := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), cur))
	require.Equal(t, metav1.ConditionTrue, condStatus(cur, EgressPolicyConditionCompiled).Status)

	// A second server joins the group: the backstop widens and core hears
	// about the new member.
	require.NoError(t, k8sClient.Create(ctx, labeledServer("m2", nsName, gold)))
	assert.True(t, waitFor(deadline, func() bool {
		return assert.ObjectsAreEqual([]string{"m1", "m2"}, backstopProviders(t, "pol", nsName))
	}), "the backstop did not widen to the new member: %v", backstopProviders(t, "pol", nsName))
	assert.True(t, pushes.waitForPush("m2", 0, deadline), "no L7 push for the new member")

	// A server that stops matching leaves the backstop.
	m2 := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "m2", Namespace: nsName}, m2))
	m2.Labels = map[string]string{"tier": "silver"}
	require.NoError(t, k8sClient.Update(ctx, m2))
	assert.True(t, waitFor(deadline, func() bool {
		return assert.ObjectsAreEqual([]string{"m1"}, backstopProviders(t, "pol", nsName))
	}), "the backstop kept a server that left the group: %v", backstopProviders(t, "pol", nsName))

	// Deleted and recreated under the same name: core dropped its L7 policy
	// with the server, so the member must be pushed again.
	require.NoError(t, k8sClient.Delete(ctx, m2))
	require.True(t, waitFor(deadline, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(m2), &mcpv1alpha2.MCPServer{}))
	}))
	before := pushes.get("m2")
	require.NoError(t, k8sClient.Create(ctx, labeledServer("m2", nsName, gold)))
	assert.True(t, pushes.waitForPush("m2", before, deadline), "no L7 push for the recreated member")
	assert.True(t, waitFor(deadline, func() bool {
		return assert.ObjectsAreEqual([]string{"m1", "m2"}, backstopProviders(t, "pol", nsName))
	}), "the backstop did not take the recreated member back: %v", backstopProviders(t, "pol", nsName))

	// A selector edit on the group recomputes the members.
	grp := &mcpv1alpha2.MCPServerGroup{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "grp", Namespace: nsName}, grp))
	grp.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "platinum"}}
	require.NoError(t, k8sClient.Update(ctx, grp))
	assert.True(t, waitFor(deadline, func() bool {
		return backstopProviders(t, "pol", nsName) == nil
	}), "the backstop outlived a selector edit that matches no server")
}

// A policy that names its server: the server deleted and recreated under the
// same name comes back without core's L7 policy, and must be pushed again.
func TestEgressPolicy_ServerRecreated_RepushesL7Policy(t *testing.T) {
	const nsName = "egress-server-recreated"
	ensureNamespace(t, nsName)
	const deadline = 10 * time.Second

	pushes, core := newL7PushLog(t)
	startEgressManager(t, core.URL)

	require.NoError(t, k8sClient.Create(ctx, testServer("srv", nsName)))
	createPolicyWithCleanup(t, testPolicy("pol", nsName))
	require.True(t, pushes.waitForPush("srv", 0, 30*time.Second), "the policy was never delivered")
	pushes.waitQuiet(t, 2*time.Second, 30*time.Second)
	settled := pushes.get("srv")

	// A status-only write on the server is not a reason to push.
	srv := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: "srv", Namespace: nsName}, srv))
	srv.Status.ToolsCount = 7
	require.NoError(t, k8sClient.Status().Update(ctx, srv))
	time.Sleep(2 * time.Second)
	assert.Equal(t, settled, pushes.get("srv"), "a status-only update re-delivered the policy")

	require.NoError(t, k8sClient.Delete(ctx, srv))
	require.True(t, waitFor(deadline, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(srv), &mcpv1alpha2.MCPServer{}))
	}))
	require.NoError(t, k8sClient.Create(ctx, testServer("srv", nsName)))
	assert.True(t, pushes.waitForPush("srv", settled, deadline), "the recreated server did not get its L7 policy back")
}

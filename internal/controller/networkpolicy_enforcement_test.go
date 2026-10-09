package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// egressServer is an MCPServer that declares egress, so it gets a per-server
// NetworkPolicy and is subject to capability_drift detection.
func egressServer(name, namespace string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
			Capabilities: &mcpv1alpha2.MCPServerCapabilities{
				Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
					Egress: []mcpv1alpha2.EgressRuleSpec{
						{Host: "10.0.0.0/8", Port: 443, Protocol: "https"},
					},
				},
			},
		},
	}
}

// createEgressServer creates srv (and its namespace) in the envtest apiserver.
func createEgressServer(t *testing.T, srv *mcpv1alpha2.MCPServer) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: srv.Namespace}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	require.NoError(t, k8sClient.Create(ctx, srv))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, srv) })
}

// liveProbe is a fresh probe against the envtest apiserver. Fresh per test so
// the TTL cache cannot carry one test's verdict into the next.
func liveProbe() *networkpolicy.EnforcementProbe {
	return &networkpolicy.EnforcementProbe{Mapper: k8sClient.RESTMapper(), Reader: k8sClient}
}

func countEvents(rec *fakeEventRecorder, reason string) int {
	n := 0
	for _, e := range rec.events {
		if strings.Contains(e, " "+reason+" ") {
			n++
		}
	}
	return n
}

// The #199 cluster: envtest serves no policy-enforcing API and runs no CNI
// agent. The per-server policy is still written, and the server must not claim
// it is applied in the sense anyone reads: False/PolicyWrittenUnenforced, one
// Warning on the transition, and no capability_drift spam on every reconcile.
func TestMCPServerNetworkPolicy_NoEnforcer_ReportsUnenforced(t *testing.T) {
	srv := egressServer("np-unenforced-199", "np-enforcement-199")
	createEgressServer(t, srv)

	rec := &fakeEventRecorder{}
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: rec, EnforcementProbe: liveProbe()}

	require.NoError(t, r.reconcileNetworkPolicy(ctx, srv))
	require.NoError(t, r.reconcileViolationDetection(ctx, srv))

	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{
		Namespace: srv.Namespace, Name: networkpolicy.NetworkPolicyName(srv.Name),
	}, &np), "the policy is still written -- only the claim about it changes")

	cond := getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonPolicyWrittenUnenforced, cond.Reason)
	assert.Equal(t, 1, countEvents(rec, "NetworkPolicyUnenforced"))
	assert.Empty(t, srv.Status.Violations, "an unenforced policy is not capability drift")

	// Second pass (the update path): no second Warning.
	require.NoError(t, r.reconcileNetworkPolicy(ctx, srv))
	assert.Equal(t, 1, countEvents(rec, "NetworkPolicyUnenforced"), "the Warning fires on the transition only")
	assert.Equal(t, ReasonPolicyWrittenUnenforced,
		getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied).Reason)
}

// With a CNI agent running, the same write is reported as applied.
func TestMCPServerNetworkPolicy_EnforcerObserved_ReportsApplied(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "cilium"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"k8s-app": "cilium"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "cilium"}}},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, ds))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, ds) })

	srv := egressServer("np-enforced-199", "np-enforcement-199")
	createEgressServer(t, srv)

	rec := &fakeEventRecorder{}
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: rec, EnforcementProbe: liveProbe()}
	require.NoError(t, r.reconcileNetworkPolicy(ctx, srv))

	cond := getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "PolicyApplied", cond.Reason)
	assert.Contains(t, cond.Message, "Cilium")
	assert.Zero(t, countEvents(rec, "NetworkPolicyUnenforced"))
}

// A probe that cannot answer (here: none wired) is doubt, not a claim: the
// condition is Unknown, not True, and doubt is not drift.
func TestMCPServerNetworkPolicy_UnknownEnforcement_ReportsUnverified(t *testing.T) {
	srv := egressServer("np-unverified-199", "default")
	r, rec := newViolationTestReconciler(srv)
	r.EnforcementProbe = nil

	require.NoError(t, r.reconcileNetworkPolicy(ctx, srv))
	require.NoError(t, r.reconcileViolationDetection(ctx, srv))

	cond := getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, ReasonPolicyWrittenUnverified, cond.Reason)
	assert.Zero(t, countEvents(rec, "NetworkPolicyUnenforced"))
	assert.Empty(t, srv.Status.Violations)
}

// The namespace default-deny has no status to carry a condition, so writing it
// where nothing enforces it is said on the Namespace as a Warning (#199). A
// fake client keeps the suite manager's own NamespaceEgressReconciler from
// racing this one.
func TestNamespaceEgress_NoEnforcer_WarnsOnWrite(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "ns-unenforced-199",
		UID:    "ns-uid-199",
		Labels: map[string]string{networkpolicy.EnforceEgressLabel: "true"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(ns).Build()

	for _, tc := range []struct {
		verdict networkpolicy.EnforcementVerdict
		warns   int
	}{
		{networkpolicy.EnforcementNotObserved, 1},
		{networkpolicy.EnforcementObserved, 0},
		{networkpolicy.EnforcementUnknown, 0},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			// Start from no policy so every case takes the create path.
			np := networkpolicy.BuildNamespaceDefaultDenyEgress(ns.Name)
			_ = c.Delete(ctx, np)

			rec := &fakeEventRecorder{}
			r := &NamespaceEgressReconciler{
				Client: c, Scheme: scheme.Scheme, Recorder: rec,
				EnforcementProbe: &networkpolicy.EnforcementProbe{Override: tc.verdict},
			}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, defaultDenyKey(ns.Name), np), "the default-deny is written either way")
			assert.Equal(t, tc.warns, countEvents(rec, ReasonDefaultDenyUnenforced))

			// The idempotent pass writes nothing and says nothing.
			_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
			require.NoError(t, err)
			assert.Equal(t, tc.warns, countEvents(rec, ReasonDefaultDenyUnenforced))
		})
	}
}

// Through the full Reconcile the condition is persisted between passes, which
// is what keeps the Warning to the transition in a running operator, and the
// apiserver accepts the new False/Unknown condition values (#174's lesson).
func TestMCPServerNetworkPolicy_NoEnforcer_FullReconcileWarnsOnce(t *testing.T) {
	srv := egressServer("np-full-199", "np-enforcement-199")
	createEgressServer(t, srv)
	key := types.NamespacedName{Name: srv.Name, Namespace: srv.Namespace}
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPServer{}
		if err := k8sClient.Get(ctx, key, cur); err != nil {
			return
		}
		cur.Finalizers = nil
		_ = k8sClient.Update(ctx, cur)
	})

	rec := &fakeEventRecorder{}
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: rec, EnforcementProbe: liveProbe()}
	// Finalizer, Pod creation, then a pass over the existing Pod.
	for range 3 {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
	}

	stored := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, key, stored))
	cond := getCondition(stored.Status.Conditions, ConditionNetworkPolicyApplied)
	require.NotNil(t, cond, "the condition is persisted")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonPolicyWrittenUnenforced, cond.Reason)
	assert.Equal(t, 1, countEvents(rec, "NetworkPolicyUnenforced"), "one Warning across reconciles")
	assert.Empty(t, stored.Status.Violations)
}

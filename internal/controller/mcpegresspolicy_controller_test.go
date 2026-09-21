package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

func newEgressReconciler(objs ...runtime.Object) *MCPEgressPolicyReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha2.AddToScheme(scheme)
	_ = mcpv1alpha2.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&mcpv1alpha2.MCPEgressPolicy{}).
		Build()

	return &MCPEgressPolicyReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}
}

func testServer(name, ns string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "img@sha256:abc"},
	}
}

func testPolicy(name, ns string) *mcpv1alpha2.MCPEgressPolicy {
	return &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			TargetRef: mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: "srv"},
		},
	}
}

func reconcilePolicy(t *testing.T, r *MCPEgressPolicyReconciler, p *mcpv1alpha2.MCPEgressPolicy) *mcpv1alpha2.MCPEgressPolicy {
	t.Helper()
	ctx := context.Background()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace}})
	require.NoError(t, err)
	out := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(ctx, types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, out))
	return out
}

func condStatus(p *mcpv1alpha2.MCPEgressPolicy, condType string) *metav1.Condition {
	return meta.FindStatusCondition(p.Status.Conditions, condType)
}

func getBackstop(t *testing.T, r *MCPEgressPolicyReconciler, name, ns string) (*networkingv1.NetworkPolicy, error) {
	np := &networkingv1.NetworkPolicy{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      networkpolicy.EgressPolicyBackstopName(name),
		Namespace: ns,
	}, np)
	return np, err
}

func TestEgressPolicy_CIDRUpstream_AppliesBackstop(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
	}
	r := newEgressReconciler(testServer("srv", "default"), p)

	out := reconcilePolicy(t, r, p)

	np, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err, "backstop should be created")
	assert.Equal(t, "srv", np.Spec.PodSelector.MatchLabels[networkpolicy.LabelProvider])
	assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	// DNS rule + the CIDR upstream rule.
	assert.Len(t, np.Spec.Egress, 2)

	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionCompiled).Status)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
}

func TestEgressPolicy_FQDNUpstream_DegradedUnenforceable(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "gh", Match: mcpv1alpha2.UpstreamMatch{Host: "api.github.com"}},
	}
	r := newEgressReconciler(testServer("srv", "default"), p)

	out := reconcilePolicy(t, r, p)

	np, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err)
	// FQDN cannot be enforced -> only the DNS rule, host is NOT opened.
	assert.Len(t, np.Spec.Egress, 1, "FQDN upstream must not add an egress rule")

	deg := condStatus(out, EgressPolicyConditionDegraded)
	require.NotNil(t, deg)
	assert.Equal(t, metav1.ConditionTrue, deg.Status)
	assert.Equal(t, "FQDNUpstreamsUnenforceable", deg.Reason)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
}

func TestEgressPolicy_TargetNotFound_WithholdsBackstop(t *testing.T) {
	p := testPolicy("pol", "default")
	r := newEgressReconciler(p) // no MCPServer

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.True(t, err != nil, "no backstop when target missing")
	assert.Equal(t, "TargetNotFound", condStatus(out, EgressPolicyConditionCompiled).Reason)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionDegraded).Status)
}

func TestEgressPolicy_GenerateFalse_NoBackstop(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.NetworkBackstop = &mcpv1alpha2.NetworkBackstop{Generate: false}
	r := newEgressReconciler(testServer("srv", "default"), p)

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.True(t, err != nil, "no backstop when generation disabled")
	ba := condStatus(out, EgressPolicyConditionBackstopApplied)
	assert.Equal(t, metav1.ConditionFalse, ba.Status)
	assert.Equal(t, "BackstopGenerationDisabled", ba.Reason)
}

func TestEgressPolicy_GroupTarget_NotFound(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	r := newEgressReconciler(p) // no group

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.True(t, err != nil)
	assert.Equal(t, "TargetNotFound", condStatus(out, EgressPolicyConditionCompiled).Reason)
}

func testGroup(name, ns string, matchLabels map[string]string) *mcpv1alpha2.MCPServerGroup {
	return &mcpv1alpha2.MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       mcpv1alpha2.MCPServerGroupSpec{Selector: &metav1.LabelSelector{MatchLabels: matchLabels}},
	}
}

func labeledServer(name, ns string, labels map[string]string) *mcpv1alpha2.MCPServer {
	s := testServer(name, ns)
	s.Labels = labels
	return s
}

func TestEgressPolicy_GroupTarget_AppliesBackstopForMembers(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}}}

	group := testGroup("grp", "default", map[string]string{"team": "x"})
	member1 := labeledServer("srv-b", "default", map[string]string{"team": "x"})
	member2 := labeledServer("srv-a", "default", map[string]string{"team": "x"})
	outsider := labeledServer("srv-c", "default", map[string]string{"team": "y"})

	r := newEgressReconciler(group, member1, member2, outsider, p)
	out := reconcilePolicy(t, r, p)

	np, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err)
	// PodSelector targets the group's members via provider In [sorted names].
	require.Len(t, np.Spec.PodSelector.MatchExpressions, 1)
	expr := np.Spec.PodSelector.MatchExpressions[0]
	assert.Equal(t, networkpolicy.LabelProvider, expr.Key)
	assert.Equal(t, metav1.LabelSelectorOpIn, expr.Operator)
	assert.Equal(t, []string{"srv-a", "srv-b"}, expr.Values) // sorted, excludes the outsider
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
}

func TestEgressPolicy_GroupTarget_NoMembers(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	group := testGroup("grp", "default", map[string]string{"team": "empty"})

	r := newEgressReconciler(group, p)
	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.True(t, err != nil, "no backstop when the group has no members")
	assert.Equal(t, "TargetNotFound", condStatus(out, EgressPolicyConditionBackstopApplied).Reason)
}

// Flipping an upstream host from FQDN to CIDR clears the degraded condition and
// updates the backstop.
func TestEgressPolicy_UpdateFromFQDNToCIDR(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "api.example.com"}}}
	r := newEgressReconciler(testServer("srv", "default"), p)

	out := reconcilePolicy(t, r, p)
	require.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionDegraded).Status)

	// Update the stored policy to a CIDR host and re-reconcile.
	out.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "192.168.0.0/16"}}}
	require.NoError(t, r.Update(context.Background(), out))

	out2 := reconcilePolicy(t, r, out)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out2, EgressPolicyConditionDegraded).Status)
	np, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err)
	assert.Len(t, np.Spec.Egress, 2, "CIDR upstream should add an egress rule")
}

// withProbe gives a reconciler an enforcement verdict without a cluster to
// probe, using the flag override path.
func withProbe(r *MCPEgressPolicyReconciler, verdict networkpolicy.EnforcementVerdict) *MCPEgressPolicyReconciler {
	r.EnforcementProbe = &networkpolicy.EnforcementProbe{Override: verdict}
	return r
}

// The #172 cluster: the backstop is written, the API server accepts it, and
// nothing in that API server will ever read it. Before this, the policy was
// indistinguishable from one being enforced.
func TestEgressPolicy_NoEnforcerObserved_ReportsUnenforced(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
	}
	r := withProbe(newEgressReconciler(testServer("srv", "default"), p), networkpolicy.EnforcementNotObserved)

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err, "the backstop is still written -- only the claim about it changes")

	assert.Equal(t, mcpv1alpha2.BackstopUnenforced, out.Status.BackstopEnforcement)
	enforceable := condStatus(out, EgressPolicyConditionBackstopEnforceable)
	require.NotNil(t, enforceable)
	assert.Equal(t, metav1.ConditionFalse, enforceable.Status)
	assert.Equal(t, "NoEnforcerObserved", enforceable.Reason)

	// BackstopApplied still reports what it means: the object was written.
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)

	deg := condStatus(out, EgressPolicyConditionDegraded)
	require.NotNil(t, deg)
	assert.Equal(t, metav1.ConditionTrue, deg.Status, "an unread backstop is a degradation, not a success")
	assert.Equal(t, "EnforcementNotObserved", deg.Reason)
}

func TestEgressPolicy_EnforcerObserved_ReportsEnforcing(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
	}
	r := withProbe(newEgressReconciler(testServer("srv", "default"), p), networkpolicy.EnforcementObserved)

	out := reconcilePolicy(t, r, p)

	assert.Equal(t, mcpv1alpha2.BackstopEnforcing, out.Status.BackstopEnforcement)
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopEnforceable).Status)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
}

// Doubt must not page anyone: a probe that could not answer leaves the policy
// undegraded and says so on its own condition.
func TestEgressPolicy_EnforcementUnknown_IsNotDegraded(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
	}
	r := newEgressReconciler(testServer("srv", "default"), p) // no probe wired

	out := reconcilePolicy(t, r, p)

	assert.Equal(t, mcpv1alpha2.BackstopUnverified, out.Status.BackstopEnforcement)
	assert.Equal(t, metav1.ConditionUnknown, condStatus(out, EgressPolicyConditionBackstopEnforceable).Status)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)
}

// An unmatched FQDN upstream is a hole in a policy that works; an unread
// NetworkPolicy is the whole policy missing. The second reason is the one to
// show.
func TestEgressPolicy_UnenforcedOutranksFQDNDegradation(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "gh", Match: mcpv1alpha2.UpstreamMatch{Host: "api.github.com"}},
	}
	r := withProbe(newEgressReconciler(testServer("srv", "default"), p), networkpolicy.EnforcementNotObserved)

	out := reconcilePolicy(t, r, p)

	assert.Equal(t, "EnforcementNotObserved", condStatus(out, EgressPolicyConditionDegraded).Reason)
}

// Nothing was asked for, so nothing is missing: the paths that deliberately
// write no backstop must not send anyone hunting for a CNI.
func TestEgressPolicy_NoBackstopPaths_ReportTheirOwnState(t *testing.T) {
	disabled := testPolicy("disabled", "default")
	disabled.Spec.NetworkBackstop = &mcpv1alpha2.NetworkBackstop{Generate: false}
	r := withProbe(newEgressReconciler(testServer("srv", "default"), disabled), networkpolicy.EnforcementNotObserved)
	out := reconcilePolicy(t, r, disabled)
	assert.Equal(t, mcpv1alpha2.BackstopDisabled, out.Status.BackstopEnforcement)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status)

	missing := testPolicy("missing", "default")
	r2 := withProbe(newEgressReconciler(missing), networkpolicy.EnforcementNotObserved) // no MCPServer
	out2 := reconcilePolicy(t, r2, missing)
	assert.Equal(t, mcpv1alpha2.BackstopPending, out2.Status.BackstopEnforcement)
	assert.Equal(t, "TargetNotFound", condStatus(out2, EgressPolicyConditionDegraded).Reason)
}

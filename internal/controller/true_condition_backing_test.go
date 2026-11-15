package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// Epic #323: a condition the reconciler sets to True must be backed by an
// object that does what the condition says. Each test below reconciles to
// True and then checks the object itself: it exists and is controlled by the
// resource, its selector matches the labels the operator's own Deployment puts
// on the pod (and not another server's pod, so neither "nothing" nor
// "everything"), its policy type is the one claimed, and its rules are the
// declared ones with no allow-all rule. Each also tampers with the object and
// requires the next True to come only with the object repaired.

// podLabels returns the labels on the pod the operator runs for s, from the
// same builder the reconciler uses, so a selector is tested against the pod
// rather than against itself.
func podLabels(t *testing.T, s *mcpv1alpha2.MCPServer) labels.Set {
	t.Helper()
	d, err := provider.BuildDeploymentForMCPServer(s)
	require.NoError(t, err)
	require.NotEmpty(t, d.Spec.Template.Labels)
	return labels.Set(d.Spec.Template.Labels)
}

// requireSelects fails unless sel is a non-empty selector that matches every
// pod in want and none in other.
func requireSelects(t *testing.T, sel metav1.LabelSelector, want, other []labels.Set) {
	t.Helper()
	require.False(t, len(sel.MatchLabels)+len(sel.MatchExpressions) == 0,
		"an empty selector selects every pod in the namespace, not the target")
	s, err := metav1.LabelSelectorAsSelector(&sel)
	require.NoError(t, err)
	for _, l := range want {
		require.True(t, s.Matches(l), "selector %s does not select the target pod %v", s, l)
	}
	for _, l := range other {
		require.False(t, s.Matches(l), "selector %s also selects a pod it does not govern: %v", s, l)
	}
}

// requireEgressBackstop checks a NetworkPolicy that claims to restrict egress.
func requireEgressBackstop(t *testing.T, np *networkingv1.NetworkPolicy, owner client.Object,
	want, other []labels.Set, cidrs ...string) {
	t.Helper()
	require.True(t, metav1.IsControlledBy(np, owner), "the policy is not controlled by %s", owner.GetName())
	require.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress,
		"without the Egress type the policy restricts no egress")
	requireSelects(t, np.Spec.PodSelector, want, other)
	got := map[string]bool{}
	for _, rule := range np.Spec.Egress {
		require.False(t, len(rule.To) == 0 && len(rule.Ports) == 0, "an empty egress rule allows all egress")
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				got[peer.IPBlock.CIDR] = true
			}
		}
	}
	for _, c := range cidrs {
		require.True(t, got[c], "the declared upstream %s has no egress rule", c)
	}
}

func requireBackstopEnforcing(t *testing.T, out *mcpv1alpha2.MCPEgressPolicy) {
	t.Helper()
	requireBackstopState(t, out, mcpv1alpha2.BackstopEnforcing)
}

// requireBackstopState is requireBackstopEnforcing for a verdict that may be
// Partial: a group with remote members is enforced for its container members
// only (#410).
func requireBackstopState(t *testing.T, out *mcpv1alpha2.MCPEgressPolicy, want mcpv1alpha2.BackstopEnforcement) {
	t.Helper()
	require.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
	require.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopEnforceable).Status)
	require.Equal(t, want, out.Status.BackstopEnforcement)
}

// openUp plays another actor: the policy selects every pod, restricts
// ingress only and allows all egress. Each is a way for it to stop
// restricting the target's egress.
func openUp(np *networkingv1.NetworkPolicy) {
	np.Spec.PodSelector = metav1.LabelSelector{}
	np.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
}

func TestTrueCondition_VanillaBackstop_IsASelectingNetworkPolicy(t *testing.T) {
	const cidr = "10.0.0.0/8"
	team := map[string]string{"team": "x"}
	outsider := labeledServer("outsider", "default", map[string]string{"team": "y"})

	for _, tc := range []struct {
		name    string
		target  mcpv1alpha2.EgressTargetRef
		objs    []runtime.Object
		members []*mcpv1alpha2.MCPServer // the container servers whose pods must be selected
		state   mcpv1alpha2.BackstopEnforcement
	}{
		{
			name:    "server",
			target:  mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: "srv"},
			members: []*mcpv1alpha2.MCPServer{testServer("srv", "default")},
		},
		{
			name:    "group",
			target:  mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"},
			objs:    []runtime.Object{testGroup("grp", "default", team)},
			members: []*mcpv1alpha2.MCPServer{labeledServer("m1", "default", team), labeledServer("m2", "default", team)},
		},
		{
			name:   "group with a remote member",
			target: mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"},
			objs: []runtime.Object{testGroup("grp", "default", team),
				remoteServer("a-remote", "default", team)},
			members: []*mcpv1alpha2.MCPServer{labeledServer("b-container", "default", team)},
			// The remote member runs no pod to select, so the backstop
			// covers part of the group and must not claim all of it.
			state: mcpv1alpha2.BackstopPartial,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.state == "" {
				tc.state = mcpv1alpha2.BackstopEnforcing
			}
			p := cidrPolicy("pol")
			p.Spec.TargetRef = tc.target
			objs := append([]runtime.Object{p, outsider}, tc.objs...)
			var want []labels.Set
			for _, m := range tc.members {
				objs = append(objs, m)
				want = append(want, podLabels(t, m))
			}
			other := []labels.Set{podLabels(t, outsider)}
			r := withProbe(newEgressReconciler(objs...), networkpolicy.EnforcementObserved)

			out := reconcilePolicy(t, r, p)
			requireBackstopState(t, out, tc.state)
			np, err := getBackstop(t, r, "pol", "default")
			require.NoError(t, err, "Enforcing over a NetworkPolicy that does not exist")
			requireEgressBackstop(t, np, out, want, other, cidr)

			openUp(np)
			require.NoError(t, r.Update(context.Background(), np, client.FieldOwner("someone-else")))

			out = reconcilePolicy(t, r, p)
			requireBackstopState(t, out, tc.state)
			np, err = getBackstop(t, r, "pol", "default")
			require.NoError(t, err)
			requireEgressBackstop(t, np, out, want, other, cidr)
		})
	}
}

// ciliumSpec reads back the parts of a CiliumNetworkPolicy that decide what it
// restricts.
func ciliumSpec(t *testing.T, cnp *unstructured.Unstructured) (metav1.LabelSelector, []interface{}) {
	t.Helper()
	raw, found, err := unstructured.NestedMap(cnp.Object, "spec", "endpointSelector")
	require.NoError(t, err)
	require.True(t, found, "a CiliumNetworkPolicy without an endpointSelector")
	var sel metav1.LabelSelector
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel))
	egress, _, err := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	require.NoError(t, err)
	return sel, egress
}

func requireCiliumBackstop(t *testing.T, r *MCPEgressPolicyReconciler, p, out *mcpv1alpha2.MCPEgressPolicy,
	want, other []labels.Set, cidr string) {
	t.Helper()
	cnp := newCiliumBackstopStub(p)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(cnp), cnp),
		"Enforcing over a CiliumNetworkPolicy that does not exist")
	require.True(t, metav1.IsControlledBy(cnp, out), "the CiliumNetworkPolicy is not controlled by the policy")
	sel, egress := ciliumSpec(t, cnp)
	requireSelects(t, sel, want, other)
	require.NotEmpty(t, egress, "a CiliumNetworkPolicy with no egress rules does not restrict egress")
	found := false
	for _, rule := range egress {
		m, ok := rule.(map[string]interface{})
		require.True(t, ok)
		require.NotEmpty(t, m, "an empty egress rule allows all egress")
		if cidrs, ok := m["toCIDR"].([]interface{}); ok {
			for _, c := range cidrs {
				found = found || c == cidr
			}
		}
	}
	require.True(t, found, "the declared upstream %s has no toCIDR rule", cidr)
}

func TestTrueCondition_CiliumBackstop_IsASelectingCiliumPolicy(t *testing.T) {
	const cidr = "10.0.0.0/8"
	p := cidrPolicy("pol")
	srv := testServer("srv", "default")
	outsider := testServer("outsider", "default")
	r := newCiliumCRDReconciler(t, srv, outsider, p, agentDaemonSet("cilium"))
	want, other := []labels.Set{podLabels(t, srv)}, []labels.Set{podLabels(t, outsider)}

	out := reconcilePolicy(t, r, p)
	requireBackstopEnforcing(t, out)
	assert.Contains(t, condStatus(out, EgressPolicyConditionBackstopApplied).Message, "Cilium")
	requireCiliumBackstop(t, r, p, out, want, other, cidr)

	// Another actor empties the selector (every pod) and drops the rules.
	cnp := newCiliumBackstopStub(p)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(cnp), cnp))
	cnp.Object["spec"] = map[string]interface{}{"endpointSelector": map[string]interface{}{}}
	require.NoError(t, r.Update(context.Background(), cnp, client.FieldOwner("someone-else")))

	out = reconcilePolicy(t, r, p)
	requireBackstopEnforcing(t, out)
	requireCiliumBackstop(t, r, p, out, want, other, cidr)
}

func TestTrueCondition_ServerNetworkPolicy_IsASelectingNetworkPolicy(t *testing.T) {
	const cidr = "203.0.113.0/24"
	srv := newTestProvider("srv", "default", &mcpv1alpha2.MCPServerCapabilities{
		Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
			Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "api.example.com", CIDR: cidr, Port: 443, Protocol: "https"}},
		},
	})
	outsider := testServer("outsider", "default")
	r := newTestReconciler(srv) // an observed enforcer
	want, other := []labels.Set{podLabels(t, srv)}, []labels.Set{podLabels(t, outsider)}
	key := types.NamespacedName{Name: networkpolicy.NetworkPolicyName(srv.Name), Namespace: srv.Namespace}

	check := func() {
		t.Helper()
		cond := getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied)
		require.NotNil(t, cond)
		require.Equal(t, metav1.ConditionTrue, cond.Status)
		np := &networkingv1.NetworkPolicy{}
		require.NoError(t, r.Get(context.Background(), key, np), "NetworkPolicyApplied=True over a policy that does not exist")
		requireEgressBackstop(t, np, srv, want, other, cidr)
	}

	require.NoError(t, r.reconcileNetworkPolicy(context.Background(), srv))
	check()

	np := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(), key, np))
	openUp(np)
	require.NoError(t, r.Update(context.Background(), np, client.FieldOwner("someone-else")))

	require.NoError(t, r.reconcileNetworkPolicy(context.Background(), srv))
	check()
}

func TestTrueCondition_ProviderIngress_IsAGatewayOnlyFence(t *testing.T) {
	const gatewayNS = "gateway"
	srv := testServer("srv", "default")
	srv.UID = "server-uid"
	outsider := testServer("outsider", "default")
	r := newTestReconciler(srv) // an observed enforcer
	var err error
	r.ProviderIngress, err = networkpolicy.ParseProviderIngressConfig(true, gatewayNS, "app=gateway")
	require.NoError(t, err)
	want, other := []labels.Set{podLabels(t, srv)}, []labels.Set{podLabels(t, outsider)}
	key := types.NamespacedName{Name: networkpolicy.ProviderIngressPolicyName(srv.Name), Namespace: srv.Namespace}

	gatewayNSLabels := labels.Set{corev1.LabelMetadataName: gatewayNS}
	tenantNSLabels := labels.Set{corev1.LabelMetadataName: "tenant"}
	gatewayPod := labels.Set{"app": "gateway"}
	otherPod := labels.Set{"app": "tenant-workload"}

	check := func() {
		t.Helper()
		cond := getCondition(srv.Status.Conditions, ConditionProviderIngressApplied)
		require.NotNil(t, cond)
		require.Equal(t, metav1.ConditionTrue, cond.Status)
		np := &networkingv1.NetworkPolicy{}
		require.NoError(t, r.Get(context.Background(), key, np), "ProviderIngressApplied=True over a policy that does not exist")
		require.True(t, metav1.IsControlledBy(np, srv))
		require.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress,
			"without the Ingress type the policy fences nothing")
		requireSelects(t, np.Spec.PodSelector, want, other)
		require.NotEmpty(t, np.Spec.Ingress, "a fence with no ingress rule is deny-all, not the claimed gateway path")
		for _, rule := range np.Spec.Ingress {
			require.NotEmpty(t, rule.From, "an ingress rule without peers admits every source")
			for _, peer := range rule.From {
				// Both selectors in ONE peer: they are ANDed. Split over two
				// peers they are ORed, and a gateway-labelled pod in any
				// namespace, or any pod in the gateway namespace, gets in.
				require.NotNil(t, peer.NamespaceSelector, "a peer with no namespace selector admits pods from every namespace")
				require.NotNil(t, peer.PodSelector, "a peer with no pod selector admits every pod in the namespace")
				requireSelects(t, *peer.NamespaceSelector, []labels.Set{gatewayNSLabels}, []labels.Set{tenantNSLabels})
				requireSelects(t, *peer.PodSelector, []labels.Set{gatewayPod}, []labels.Set{otherPod})
			}
			require.Len(t, rule.Ports, 1)
			require.Equal(t, int32(provider.DefaultDiscoveryPort), rule.Ports[0].Port.IntVal)
			require.Equal(t, corev1.ProtocolTCP, *rule.Ports[0].Protocol)
		}
	}

	require.NoError(t, r.reconcileProviderIngress(context.Background(), srv))
	check()

	np := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(), key, np))
	np.Spec.PodSelector = metav1.LabelSelector{}
	np.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	require.NoError(t, r.Update(context.Background(), np, client.FieldOwner("someone-else")))

	require.NoError(t, r.reconcileProviderIngress(context.Background(), srv))
	check()
}

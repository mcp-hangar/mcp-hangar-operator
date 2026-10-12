package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// The operator owns the NetworkPolicies it writes, not every field on them.
// It used to write them with a full Update that replaced the labels, so a
// label another actor added -- a cost-centre tag, a GitOps marker -- was gone
// after the next reconcile that had anything to fix. With Server-Side Apply
// the API server keeps what this field manager did not set (#220). These run
// against the envtest API server, which merges the way a cluster does.

const foreignLabel = "team.example.com/owner"

// driftWithForeignLabel plays another actor: it adds a label and opens the
// policy's egress, as one Update under its own field manager.
func driftWithForeignLabel(t *testing.T, key types.NamespacedName) {
	t.Helper()
	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, key, &np))
	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[foreignLabel] = "payments"
	np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}} // allow all
	require.NoError(t, k8sClient.Update(ctx, &np, client.FieldOwner("someone-else")))
}

func TestNamespaceDefaultDeny_ForeignLabelSurvivesTheRepair(t *testing.T) {
	const nsName = "ns-egress-foreign-label"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsName,
		Labels: map[string]string{networkpolicy.EnforceEgressLabel: "true"},
	}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	key := defaultDenyKey(nsName)
	var np networkingv1.NetworkPolicy
	require.Eventually(t, func() bool { return k8sClient.Get(ctx, key, &np) == nil },
		nsEgressWait, nsEgressTick, "default-deny created")

	driftWithForeignLabel(t, key)

	wanted := networkpolicy.BuildNamespaceDefaultDenyEgress(nsName).Spec
	require.Eventually(t, func() bool {
		if err := k8sClient.Get(ctx, key, &np); err != nil {
			return false
		}
		return len(np.Spec.Egress) == len(wanted.Egress) && len(np.Spec.Egress[0].To) == len(wanted.Egress[0].To)
	}, nsEgressWait, nsEgressTick, "the opened egress is repaired")
	assert.Equal(t, "payments", np.Labels[foreignLabel], "a label the operator does not set survives its repair")
	assert.Equal(t, networkpolicy.DefaultManagerName, np.Labels[networkpolicy.LabelManagedBy])
}

func TestReconcileNetworkPolicy_ForeignLabelSurvivesTheRepair(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "np-foreign-label"}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	server := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: ns.Name},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "test:latest",
			Capabilities: &mcpv1alpha2.MCPServerCapabilities{Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
				Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "10.0.0.0/8", Port: 443, Protocol: "https"}},
			}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, server))
	defer func() { _ = k8sClient.Delete(ctx, server) }()

	r := &MCPServerReconciler{
		Client: k8sClient, Scheme: scheme.Scheme, Recorder: events.NewFakeRecorder(10),
		EnforcementProbe: &networkpolicy.EnforcementProbe{Override: networkpolicy.EnforcementObserved},
	}
	require.NoError(t, r.reconcileNetworkPolicy(ctx, server))
	key := types.NamespacedName{Name: networkpolicy.NetworkPolicyName(server.Name), Namespace: ns.Name}

	driftWithForeignLabel(t, key)
	require.NoError(t, r.reconcileNetworkPolicy(ctx, server))

	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, key, &np))
	assert.Equal(t, networkpolicy.BuildNetworkPolicy(server).Spec.Egress, np.Spec.Egress, "the opened egress is repaired")
	assert.Equal(t, "payments", np.Labels[foreignLabel], "a label the operator does not set survives its repair")
	assert.True(t, metav1.IsControlledBy(&np, server))
}

// The egress-policy backstops, on the fake client: its deduced type converter
// merges labels per key, as the API server does.
func TestEgressBackstop_ForeignLabelSurvivesTheRepair(t *testing.T) {
	p := cidrPolicy("pol")
	r := newEgressReconciler(testServer("srv", "default"), p)
	reconcilePolicy(t, r, p)

	np, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err)
	np.Labels[foreignLabel] = "payments"
	np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
	require.NoError(t, r.Update(ctx, np, client.FieldOwner("someone-else")))

	reconcilePolicy(t, r, p)

	np, err = getBackstop(t, r, "pol", "default")
	require.NoError(t, err)
	assert.NotEqual(t, []networkingv1.NetworkPolicyEgressRule{{}}, np.Spec.Egress, "the opened egress is repaired")
	assert.Equal(t, "payments", np.Labels[foreignLabel], "a label the operator does not set survives its repair")
}

func TestCiliumBackstop_ForeignLabelSurvivesTheRepair(t *testing.T) {
	p := cidrPolicy("pol")
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, agentDaemonSet("cilium"))
	reconcilePolicy(t, r, p)

	cnp := newCiliumBackstopStub(p)
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(cnp), cnp))
	labels := cnp.GetLabels()
	labels[foreignLabel] = "payments"
	cnp.SetLabels(labels)
	cnp.Object["spec"] = map[string]interface{}{"endpointSelector": map[string]interface{}{}}
	require.NoError(t, r.Update(ctx, cnp, client.FieldOwner("someone-else")))

	reconcilePolicy(t, r, p)

	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(cnp), cnp))
	assert.Contains(t, cnp.Object["spec"], "egress", "the CiliumNetworkPolicy spec is repaired")
	assert.Equal(t, "payments", cnp.GetLabels()[foreignLabel], "a label the operator does not set survives its repair")
}

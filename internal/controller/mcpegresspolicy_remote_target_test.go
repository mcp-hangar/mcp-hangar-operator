package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// A mode: remote MCPServer has no pods. Egress to its upstream leaves from the
// gateway pod, so a NetworkPolicy built from the target's selector selects
// nothing -- and the controller reported BackstopApplied=True and Enforcing
// over it (#326). It now reports that no backstop applies, writes none, and
// still delivers the L7 rules, which are the only enforcement a remote target
// has.

func remoteServer(name, ns string, labels map[string]string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "https://upstream.example.com/mcp"},
	}
}

func TestEgressPolicy_RemoteTarget_AppliesNoBackstopAndSaysSo(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}}}
	r := withProbe(newEgressReconciler(remoteServer("srv", "default", nil), p), networkpolicy.EnforcementObserved)

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.Error(t, err, "a remote target has no pods: no NetworkPolicy may be written for it")
	applied := condStatus(out, EgressPolicyConditionBackstopApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Equal(t, "RemoteTargetHasNoPods", applied.Reason)
	assert.Equal(t, mcpv1alpha2.BackstopNotApplicable, out.Status.BackstopEnforcement,
		"Enforcing over a selector that selects nothing is the over-claim")
	enforceable := condStatus(out, EgressPolicyConditionBackstopEnforceable)
	require.NotNil(t, enforceable)
	assert.Equal(t, metav1.ConditionFalse, enforceable.Status)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionDegraded).Status,
		"nothing is wrong: there is simply no backstop to apply")
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionCompiled).Status)
}

func TestEgressPolicy_RemoteTarget_RemovesABackstopLeftBehind(t *testing.T) {
	p := testPolicy("pol", "default")
	stale := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: networkpolicy.EgressPolicyBackstopName("pol"), Namespace: "default",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-hangar-operator"},
	}}
	r := withProbe(newEgressReconciler(remoteServer("srv", "default", nil), p, stale), networkpolicy.EnforcementObserved)

	reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.Error(t, err, "a backstop written before the target was seen to be remote must not outlive it")
}

func TestEgressPolicy_RemoteTarget_StillDeliversTheL7Policy(t *testing.T) {
	var mu sync.Mutex
	var pushed []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/l7_policy") {
			mu.Lock()
			pushed = append(pushed, r.URL.Path)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"l7_policy_set":true,"persisted":true}`))
	}))
	t.Cleanup(core.Close)

	p := testPolicy("pol", "default")
	r := withProbe(newEgressReconciler(remoteServer("srv", "default", nil), p), networkpolicy.EnforcementObserved)
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: core.URL, MaxRetries: 1})

	out := reconcilePolicy(t, r, p)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/api/mcp_servers/srv/l7_policy"}, pushed, "the L7 rules are the only enforcement a remote target has")
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionL7Delivered).Status)
}

func TestEgressPolicy_GroupOfOnlyRemoteServers_AppliesNoBackstop(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	group := testGroup("grp", "default", map[string]string{"team": "x"})
	r := withProbe(newEgressReconciler(group,
		remoteServer("a", "default", map[string]string{"team": "x"}),
		remoteServer("b", "default", map[string]string{"team": "x"}), p), networkpolicy.EnforcementObserved)

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	assert.Error(t, err)
	assert.Equal(t, "RemoteTargetHasNoPods", condStatus(out, EgressPolicyConditionBackstopApplied).Reason)
	assert.Equal(t, mcpv1alpha2.BackstopNotApplicable, out.Status.BackstopEnforcement)
}

func TestEgressPolicy_GroupWithAContainerMember_StillGetsABackstop(t *testing.T) {
	p := testPolicy("pol", "default")
	p.Spec.TargetRef = mcpv1alpha2.EgressTargetRef{Kind: "MCPServerGroup", Name: "grp"}
	group := testGroup("grp", "default", map[string]string{"team": "x"})
	r := withProbe(newEgressReconciler(group,
		remoteServer("a-remote", "default", map[string]string{"team": "x"}),
		labeledServer("b-container", "default", map[string]string{"team": "x"}), p), networkpolicy.EnforcementObserved)

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err, "the container member has pods to select")
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionBackstopApplied).Status)
	assert.Equal(t, mcpv1alpha2.BackstopEnforcing, out.Status.BackstopEnforcement)
}

// Against a real API server, because the status enum is part of the CRD: a
// controller that wrote a value the shipped CRD does not list would have every
// status write rejected, and the fake client used above cannot say so (#326).
func TestEgressPolicy_RemoteTarget_Envtest_StatusIsAcceptedAndNoNetworkPolicyExists(t *testing.T) {
	const nsName = "remote-backstop"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	require.NoError(t, k8sClient.Create(ctx, remoteServer("srv", nsName, nil)))
	policy := testPolicy("pol", nsName)
	require.NoError(t, k8sClient.Create(ctx, policy))

	r := &MCPEgressPolicyReconciler{
		Client:   k8sClient,
		Scheme:   k8sClient.Scheme(),
		Recorder: events.NewFakeRecorder(20),
	}
	r = withProbe(r, networkpolicy.EnforcementObserved)
	key := types.NamespacedName{Name: "pol", Namespace: nsName}
	// Twice: the first pass adds the finalizer and carries on in the same pass.
	for range 2 {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
	}

	var got mcpv1alpha2.MCPEgressPolicy
	require.NoError(t, k8sClient.Get(ctx, key, &got))
	assert.Equal(t, mcpv1alpha2.BackstopNotApplicable, got.Status.BackstopEnforcement)
	assert.Equal(t, "RemoteTargetHasNoPods", condStatus(&got, EgressPolicyConditionBackstopApplied).Reason)

	var policies networkingv1.NetworkPolicyList
	require.NoError(t, k8sClient.List(ctx, &policies, client.InNamespace(nsName)))
	for _, np := range policies.Items {
		assert.NotContains(t, np.Name, "pol", "no NetworkPolicy may exist for a remote target")
	}
}

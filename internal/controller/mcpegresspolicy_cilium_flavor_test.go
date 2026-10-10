package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// The Cilium CRDs outlive `cilium uninstall` unless purged. On such a cluster
// Auto used to pick the Cilium flavor from the CRD alone, write a
// CiliumNetworkPolicy nobody reads, delete the NetworkPolicy the real CNI
// enforces, and report Enforcing (#200).

var ciliumGV = schema.GroupVersion{Group: networkpolicy.CiliumGroup, Version: networkpolicy.CiliumVersion}

// newCiliumCRDReconciler builds a reconciler whose API server serves the
// CiliumNetworkPolicy CRD, with the probe reading the same fake API server, so
// the DaemonSets passed in are the only agents it can find.
func newCiliumCRDReconciler(t *testing.T, objs ...client.Object) *MCPEgressPolicyReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha2.AddToScheme(scheme))

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{ciliumGV})
	mapper.Add(ciliumGV.WithKind(networkpolicy.CiliumNetworkPolicyKind), meta.RESTScopeNamespace)
	for gvk := range scheme.AllKnownTypes() {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1alpha2.MCPEgressPolicy{}).
		Build()
	return &MCPEgressPolicyReconciler{
		Client:           c,
		Scheme:           scheme,
		Recorder:         events.NewFakeRecorder(10),
		EnforcementProbe: &networkpolicy.EnforcementProbe{Mapper: mapper, Reader: c},
	}
}

func agentDaemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"}}
}

func cidrPolicy(name string) *mcpv1alpha2.MCPEgressPolicy {
	p := testPolicy(name, "default")
	p.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	p.Spec.Upstreams = []mcpv1alpha2.UpstreamRule{
		{Name: "u", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
	}
	return p
}

func ciliumBackstopExists(t *testing.T, r *MCPEgressPolicyReconciler, policy string) bool {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumGV.WithKind(networkpolicy.CiliumNetworkPolicyKind))
	err := r.Get(context.Background(), types.NamespacedName{
		Name: networkpolicy.EgressPolicyBackstopName(policy), Namespace: "default",
	}, u)
	if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		t.Fatalf("get CiliumNetworkPolicy: %v", err)
	}
	return err == nil
}

func TestEgressPolicy_CiliumCRDWithoutAgent_UsesVanilla(t *testing.T) {
	p := cidrPolicy("pol")
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, agentDaemonSet("calico-node"))

	out := reconcilePolicy(t, r, p)

	_, err := getBackstop(t, r, "pol", "default")
	require.NoError(t, err, "the Vanilla NetworkPolicy is the floor the running CNI enforces")
	assert.False(t, ciliumBackstopExists(t, r, "pol"), "no CiliumNetworkPolicy for an agent that is not running")
	applied := condStatus(out, EgressPolicyConditionBackstopApplied)
	require.NotNil(t, applied)
	assert.Contains(t, applied.Message, "Vanilla")
	assert.Contains(t, applied.Message, "no cilium agent was observed")
	// Calico enforces here, so the floor is enforced -- by Calico, not Cilium.
	enforceable := condStatus(out, EgressPolicyConditionBackstopEnforceable)
	require.NotNil(t, enforceable)
	assert.Equal(t, metav1.ConditionTrue, enforceable.Status)
	assert.NotContains(t, enforceable.Message, "Cilium")
}

// Nothing runs at all: the leftover CRD must not read as an enforcer.
func TestEgressPolicy_CiliumCRDOnly_NotEnforcing(t *testing.T) {
	p := cidrPolicy("pol")
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p)

	out := reconcilePolicy(t, r, p)

	assert.False(t, ciliumBackstopExists(t, r, "pol"))
	assert.Equal(t, mcpv1alpha2.BackstopUnenforced, out.Status.BackstopEnforcement)
	assert.Equal(t, metav1.ConditionFalse, condStatus(out, EgressPolicyConditionBackstopEnforceable).Status)
}

// The healthy Cilium cluster is unchanged: CRD and agent, so the Cilium flavor.
func TestEgressPolicy_CiliumCRDAndAgent_UsesCilium(t *testing.T) {
	p := cidrPolicy("pol")
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, agentDaemonSet("cilium"))

	out := reconcilePolicy(t, r, p)

	assert.True(t, ciliumBackstopExists(t, r, "pol"))
	_, err := getBackstop(t, r, "pol", "default")
	assert.True(t, apierrors.IsNotFound(err), "the Vanilla NetworkPolicy is replaced by the Cilium one")
	assert.Equal(t, mcpv1alpha2.BackstopEnforcing, out.Status.BackstopEnforcement)
}

// An uninstall after the fact: the CNP written while Cilium ran is cleaned up
// on the next reconcile, because the CRD (and so the object) is still there.
func TestEgressPolicy_CiliumUninstalled_RemovesTheStaleCNP(t *testing.T) {
	p := cidrPolicy("pol")
	ds := agentDaemonSet("cilium")
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, ds)
	reconcilePolicy(t, r, p)
	require.True(t, ciliumBackstopExists(t, r, "pol"))

	require.NoError(t, r.Delete(context.Background(), ds))
	r.EnforcementProbe = &networkpolicy.EnforcementProbe{Mapper: r.EnforcementProbe.Mapper, Reader: r.Client}
	reconcilePolicy(t, r, p)

	assert.False(t, ciliumBackstopExists(t, r, "pol"), "the stale CiliumNetworkPolicy must be deleted")
	_, err := getBackstop(t, r, "pol", "default")
	assert.NoError(t, err, "the Vanilla floor must be back")
}

// Cilium asked for by name where it does not run: the floor, and a Degraded
// reason that is not mistaken for a missing CRD.
func TestEgressPolicy_CiliumRequestedAgentMissing_Degraded(t *testing.T) {
	p := cidrPolicy("pol")
	p.Spec.NetworkBackstop = &mcpv1alpha2.NetworkBackstop{Generate: true, Flavor: mcpv1alpha2.BackstopFlavorCilium}
	r := newCiliumCRDReconciler(t, testServer("srv", "default"), p, agentDaemonSet("calico-node"))

	out := reconcilePolicy(t, r, p)

	assert.False(t, ciliumBackstopExists(t, r, "pol"))
	degraded := condStatus(out, EgressPolicyConditionDegraded)
	require.NotNil(t, degraded)
	assert.Equal(t, metav1.ConditionTrue, degraded.Status)
	assert.Equal(t, "CiliumAgentNotObserved", degraded.Reason)
}

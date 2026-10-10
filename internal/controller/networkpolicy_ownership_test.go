package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

func foreignPolicy(name, ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: "other-uid",
				Controller: ptr.To(true),
			}},
		},
		Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	}
}

// The per-server policy update overwrote the spec of any same-named
// NetworkPolicy, whoever controlled it, and the delete path removed it (#210).
func TestReconcileNetworkPolicy_ForeignSameNamePolicyIsLeftAlone(t *testing.T) {
	srv := newTestProvider("np-foreign", "default", egressCaps())
	srv.Spec.Image = "img@sha256:abc"
	npName := networkpolicy.NetworkPolicyName("np-foreign")
	r := newTestReconciler(srv, foreignPolicy(npName, "default"))

	require.NoError(t, r.reconcileNetworkPolicy(context.Background(), srv))

	got := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: npName, Namespace: "default"}, got))
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, got.Spec.PolicyTypes, "spec untouched")
	assert.Equal(t, "someone-else", metav1.GetControllerOf(got).Name, "not adopted")
	cond := getCondition(srv.Status.Conditions, ConditionNetworkPolicyApplied)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonPolicyNameTaken, cond.Reason)

	// Dropping the capabilities must not delete it either.
	srv.Spec.Capabilities = nil
	require.NoError(t, r.reconcileNetworkPolicy(context.Background(), srv))
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: npName, Namespace: "default"}, got))
}

// A policy the operator wrote before it set owner references is still ours.
func TestReconcileNetworkPolicy_LegacyUnownedPolicyIsAdopted(t *testing.T) {
	srv := newTestProvider("np-legacy", "default", egressCaps())
	srv.Spec.Image = "img@sha256:abc"
	npName := networkpolicy.NetworkPolicyName("np-legacy")
	legacy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: npName, Namespace: "default",
		Labels: map[string]string{networkpolicy.LabelManagedBy: networkpolicy.DefaultManagerName},
	}}
	r := newTestReconciler(srv, legacy)

	require.NoError(t, r.reconcileNetworkPolicy(context.Background(), srv))

	got := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: npName, Namespace: "default"}, got))
	assert.True(t, metav1.IsControlledBy(got, srv), "adopted with an owner reference")
	assert.NotEmpty(t, got.Spec.Egress, "spec written")
}

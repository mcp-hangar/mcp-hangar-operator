package controller

import (
	"context"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"testing"
)

func TestProviderIngress_OptInOverrideAndOwnership(t *testing.T) {
	s := testServer("srv", "default")
	s.UID = "server-uid"
	r := newTestReconciler(s)
	r.ProviderIngress, _ = networkpolicy.ParseProviderIngressConfig(false, "gateway", "app=gateway")
	key := types.NamespacedName{Name: networkpolicy.ProviderIngressPolicyName(s.Name), Namespace: s.Namespace}
	read := func() error { return r.Get(context.Background(), key, &networkingv1.NetworkPolicy{}) }
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	assert.True(t, apierrors.IsNotFound(read()))
	s.Annotations = map[string]string{networkpolicy.AnnotationProviderIngress: "true"}
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	require.NoError(t, read())
	cond := getCondition(s.Status.Conditions, ConditionProviderIngressApplied)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	// Reconcile drift and ensure the owned object is restored.
	np := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(context.Background(), key, np))
	np.Spec.Ingress = nil
	require.NoError(t, r.Update(context.Background(), np))
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	require.NoError(t, r.Get(context.Background(), key, np))
	assert.Len(t, np.Spec.Ingress, 1)
	r.ProviderIngress.Enabled = true
	s.Annotations[networkpolicy.AnnotationProviderIngress] = "false"
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	assert.True(t, apierrors.IsNotFound(read()))
	np = &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	require.NoError(t, r.Create(context.Background(), np))
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	require.NoError(t, read())
	s.Annotations[networkpolicy.AnnotationProviderIngress] = "true"
	require.ErrorContains(t, r.reconcileProviderIngress(context.Background(), s), "different owner")
	require.NoError(t, read())
}

func TestProviderIngress_InvalidConfigRemoteAndEnforcement(t *testing.T) {
	for _, verdict := range []networkpolicy.EnforcementVerdict{networkpolicy.EnforcementNotObserved, networkpolicy.EnforcementUnknown} {
		s := testServer("srv", "default")
		s.UID = "server-uid"
		r := newTestReconciler(s)
		r.ProviderIngress, _ = networkpolicy.ParseProviderIngressConfig(true, "gateway", "app=gateway")
		r.EnforcementProbe = &networkpolicy.EnforcementProbe{Override: verdict}
		require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
		assert.NotEqual(t, metav1.ConditionTrue, getCondition(s.Status.Conditions, ConditionProviderIngressApplied).Status)
	}
	s := testServer("srv", "default")
	r := newTestReconciler(s)
	s.Annotations = map[string]string{networkpolicy.AnnotationProviderIngress: "true"}
	require.ErrorContains(t, r.reconcileProviderIngress(context.Background(), s), "explicit gateway namespace")
	s.Annotations[networkpolicy.AnnotationProviderIngress] = "typo"
	require.ErrorContains(t, r.reconcileProviderIngress(context.Background(), s), "must be true or false")
	s.Spec.Mode = mcpv1alpha2.MCPServerModeRemote
	require.NoError(t, r.reconcileProviderIngress(context.Background(), s))
	assert.Equal(t, "NotApplicable", getCondition(s.Status.Conditions, ConditionProviderIngressApplied).Reason)
}

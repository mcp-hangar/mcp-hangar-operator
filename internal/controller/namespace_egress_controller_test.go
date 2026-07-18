package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

func TestNamespaceEgress_CreatesAndRemovesDefaultDeny(t *testing.T) {
	r := &NamespaceEgressReconciler{Client: k8sClient}
	const nsName = "ns-egress-optin"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsName,
		Labels: map[string]string{networkpolicy.EnforceEgressLabel: "true"},
	}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: nsName}}
	key := types.NamespacedName{Namespace: nsName, Name: networkpolicy.DefaultDenyEgressName}

	// Opted in -> default-deny egress is created.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, key, &np))
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)
	assert.Empty(t, np.Spec.PodSelector.MatchLabels, "selects all pods")

	// Reconcile again is idempotent (no error, still present).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, k8sClient.Get(ctx, key, &np))

	// Opt out (remove the label) -> the policy is removed.
	current := &corev1.Namespace{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, current))
	current.Labels = map[string]string{}
	require.NoError(t, k8sClient.Update(ctx, current))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	err = k8sClient.Get(ctx, key, &np)
	assert.True(t, apierrors.IsNotFound(err), "default-deny removed once the namespace opts out")
}

func TestNamespaceEgress_UnlabeledNamespaceIsUntouched(t *testing.T) {
	r := &NamespaceEgressReconciler{Client: k8sClient}
	const nsName = "ns-egress-plain"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: nsName}})
	require.NoError(t, err)

	var np networkingv1.NetworkPolicy
	err = k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: networkpolicy.DefaultDenyEgressName}, &np)
	assert.True(t, apierrors.IsNotFound(err), "an unlabeled namespace gets no default-deny policy")
}

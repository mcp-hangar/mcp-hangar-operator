package controller

import (
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
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

const (
	nsEgressWait = 10 * time.Second
	nsEgressTick = 100 * time.Millisecond
)

func defaultDenyKey(nsName string) types.NamespacedName {
	return types.NamespacedName{Namespace: nsName, Name: networkpolicy.DefaultDenyEgressName}
}

// labelNamespace sets (or clears) the enforce-egress opt-in on a live namespace.
func labelNamespace(t *testing.T, nsName string, optIn bool) {
	t.Helper()
	current := &corev1.Namespace{}
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, current))
	if optIn {
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		current.Labels[networkpolicy.EnforceEgressLabel] = "true"
	} else {
		delete(current.Labels, networkpolicy.EnforceEgressLabel)
	}
	require.NoError(t, k8sClient.Update(ctx, current))
}

// The manager in suite_test.go runs NamespaceEgressReconciler, so these
// tests drive it through the apiserver and wait for the watch, the way a
// cluster does (#204).
func TestNamespaceEgress_CreatesAndRemovesDefaultDeny(t *testing.T) {
	const nsName = "ns-egress-optin"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsName,
		Labels: map[string]string{networkpolicy.EnforceEgressLabel: "true"},
	}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns))

	key := defaultDenyKey(nsName)
	var np networkingv1.NetworkPolicy

	// Opted in -> default-deny egress is created, controlled by the Namespace.
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, key, &np) == nil
	}, nsEgressWait, nsEgressTick, "default-deny created for an opted-in namespace")
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)
	assert.Empty(t, np.Spec.PodSelector.MatchLabels, "selects all pods")
	assert.True(t, metav1.IsControlledBy(&np, ns), "the Namespace is the controller owner (#204)")
	createdUID := np.UID

	// Delete the policy -> the Owns() watch recreates it.
	require.NoError(t, k8sClient.Delete(ctx, &np))
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, key, &np) == nil && np.UID != createdUID
	}, nsEgressWait, nsEgressTick, "default-deny recreated after deletion (#204)")
	assert.True(t, metav1.IsControlledBy(&np, ns))

	// Open the egress rules -> the watch restores DNS-only.
	wanted := networkpolicy.BuildNamespaceDefaultDenyEgress(nsName).Spec
	np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}} // allow all
	require.NoError(t, k8sClient.Update(ctx, &np))
	require.Eventually(t, func() bool {
		if err := k8sClient.Get(ctx, key, &np); err != nil {
			return false
		}
		return len(np.Spec.Egress) == len(wanted.Egress) && len(np.Spec.Egress[0].To) == len(wanted.Egress[0].To)
	}, nsEgressWait, nsEgressTick, "default-deny egress rules restored after drift (#204)")

	// Opt out (remove the label) -> the policy is removed.
	labelNamespace(t, nsName, false)
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, key, &np))
	}, nsEgressWait, nsEgressTick, "default-deny removed once the namespace opts out")
}

func TestNamespaceEgress_UnlabeledNamespaceIsUntouched(t *testing.T) {
	r := &NamespaceEgressReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: &fakeEventRecorder{}}
	const nsName = "ns-egress-plain"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: nsName}})
	require.NoError(t, err)

	var np networkingv1.NetworkPolicy
	err = k8sClient.Get(ctx, defaultDenyKey(nsName), &np)
	assert.True(t, apierrors.IsNotFound(err), "an unlabeled namespace gets no default-deny policy")
}

// A policy written by an operator before #204 has the managed-by label and no
// owner. It is ours: the upgrade adopts it and restores its spec.
//
// The pre-#204 policy is made from the operator's own one (owner stripped,
// spec drifted) rather than created before the namespace is labelled: the
// reconcile of a still-unlabelled namespace legitimately deletes a policy the
// operator owns, so creating one first raced it (#236).
func TestNamespaceEgress_AdoptsPreOwnershipPolicy(t *testing.T) {
	const nsName = "ns-egress-adopt"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   nsName,
		Labels: map[string]string{networkpolicy.EnforceEgressLabel: "true"},
	}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns))

	var np networkingv1.NetworkPolicy
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, defaultDenyKey(nsName), &np) == nil && metav1.IsControlledBy(&np, ns)
	}, nsEgressWait, nsEgressTick, "default-deny created for an opted-in namespace")

	// Turn it into what an operator before #204 wrote: no owner, and drifted.
	require.Eventually(t, func() bool {
		if err := k8sClient.Get(ctx, defaultDenyKey(nsName), &np); err != nil {
			return false
		}
		np.OwnerReferences = nil
		np.Spec.Egress = nil // drifted: deny DNS too
		return k8sClient.Update(ctx, &np) == nil
	}, nsEgressWait, nsEgressTick, "strip the owner")
	legacyUID := np.UID
	require.Equal(t, networkpolicy.DefaultManagerName, np.Labels[networkpolicy.LabelManagedBy])

	require.Eventually(t, func() bool {
		if err := k8sClient.Get(ctx, defaultDenyKey(nsName), &np); err != nil {
			return false
		}
		return metav1.IsControlledBy(&np, ns) && len(np.Spec.Egress) == 1
	}, nsEgressWait, nsEgressTick, "legacy policy adopted and its spec restored")
	assert.Equal(t, legacyUID, np.UID, "adopted in place, not replaced")
}

// A namespace that is not opted in keeps a same-named policy it does not own:
// only the operator's own default-deny is removed on opt-out (#236).
func TestNamespaceEgress_UnlabelledKeepsForeignPolicy(t *testing.T) {
	const nsName = "ns-egress-unlabelled-foreign"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	foreign := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: networkpolicy.DefaultDenyEgressName, Namespace: nsName},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "theirs"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, foreign))

	r := &NamespaceEgressReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: &fakeEventRecorder{}}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: nsName}})
	require.NoError(t, err)

	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, defaultDenyKey(nsName), &np), "a policy the operator does not own is not deleted")
	assert.Equal(t, foreign.UID, np.UID)
}

// A same-named policy controlled by something else is not adopted: it keeps
// its spec and its owner, and the Namespace gets a Warning Event (#204).
func TestNamespaceEgress_ForeignPolicyIsNotAdopted(t *testing.T) {
	const nsName = "ns-egress-foreign"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	foreignOwner := metav1.OwnerReference{
		APIVersion: mcpv1alpha2.GroupVersion.String(),
		Kind:       "MCPServer",
		Name:       "someone-else",
		UID:        types.UID("11111111-2222-3333-4444-555555555555"),
		Controller: ptr.To(true),
	}
	foreign := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            networkpolicy.DefaultDenyEgressName,
			Namespace:       nsName,
			OwnerReferences: []metav1.OwnerReference{foreignOwner},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "theirs"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, foreign))

	labelNamespace(t, nsName, true)

	rec := &fakeEventRecorder{}
	r := &NamespaceEgressReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: rec}
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: nsName}})
	require.NoError(t, err)
	assert.Equal(t, foreignDefaultDenyRequeue, res.RequeueAfter, "re-checked later; no watch fires for a foreign policy")
	require.Len(t, rec.events, 1)
	assert.Contains(t, rec.events[0], corev1.EventTypeWarning+" "+ReasonDefaultDenyNotOwned)

	// Still theirs, byte for byte where it matters.
	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, defaultDenyKey(nsName), &np))
	assert.Equal(t, []metav1.OwnerReference{foreignOwner}, np.OwnerReferences)
	assert.Equal(t, foreign.Spec, np.Spec)
	assert.NotContains(t, np.Labels, networkpolicy.LabelManagedBy)
}

// reconcileTotal sums controller_runtime_reconcile_total for one controller.
func reconcileTotal(t *testing.T, controllerName string) float64 {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	var total float64
	for _, mf := range families {
		if mf.GetName() != "controller_runtime_reconcile_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "controller" && lp.GetValue() == controllerName {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

// The MCPServer and MCPEgressPolicy controllers own NetworkPolicies too.
// Owns() routes by the controller owner's kind, so a per-server policy must
// not enqueue a Namespace reconcile, while a Namespace-owned one must (#204).
func TestNamespaceEgress_PerServerPolicyDoesNotEnqueueNamespace(t *testing.T) {
	const nsName = "ns-egress-routing"

	// Let the reconcile for the namespace creation itself land first.
	before := reconcileTotal(t, "namespace")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()
	require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns))
	require.Eventually(t, func() bool {
		return reconcileTotal(t, "namespace") > before
	}, nsEgressWait, nsEgressTick, "namespace creation reconciles")
	time.Sleep(500 * time.Millisecond)
	settled := reconcileTotal(t, "namespace")

	perServer := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-someone",
			Namespace: nsName,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: mcpv1alpha2.GroupVersion.String(),
				Kind:       "MCPServer",
				Name:       "someone",
				UID:        types.UID("66666666-7777-8888-9999-000000000000"),
				Controller: ptr.To(true),
			}},
		},
		Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}},
	}
	require.NoError(t, k8sClient.Create(ctx, perServer))
	time.Sleep(time.Second)
	assert.Equal(t, settled, reconcileTotal(t, "namespace"), "a per-server policy does not enqueue the Namespace")

	owned := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "owned-probe", Namespace: nsName},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}},
	}
	require.NoError(t, ctrl.SetControllerReference(ns, owned, scheme.Scheme))
	require.NoError(t, k8sClient.Create(ctx, owned))
	require.Eventually(t, func() bool {
		return reconcileTotal(t, "namespace") > settled
	}, nsEgressWait, nsEgressTick, "a Namespace-owned policy enqueues the Namespace")
}

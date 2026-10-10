package controller

import (
	"context"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// The enforce-egress label lives on the Namespace, but the MCPServer reconciler
// read it only when something else woke the server: its next poll, five or ten
// minutes later. Labelling a namespace left an unpinned server's egress open
// for that long, and un-labelling left it closed (#205).
//
// The suite's manager does not run the MCPServer reconciler, so this starts a
// manager of its own with only that controller, the way the operator wires it.
// Both waits are far shorter than any requeue interval, so only the Namespace
// watch can satisfy them.
func TestMCPServer_EnforceEgressLabelFlip_ReconcilesWithoutWaitingForThePoll(t *testing.T) {
	const nsName = "mcp-ns-watch-205"
	const name = "unpinned-205"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	require.NoError(t, k8sClient.Create(ctx, ns))

	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	require.NoError(t, err)
	require.NoError(t, (&MCPServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorder("mcpserver-controller-205"),
		EnforcementProbe: &networkpolicy.EnforcementProbe{Override: networkpolicy.EnforcementObserved},
	}).SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() { _ = mgr.Start(mgrCtx) }()

	server := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsName},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:         mcpv1alpha2.MCPServerModeContainer,
			Image:        "ghcr.io/org/app:latest",
			Capabilities: egressCaps(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, server))
	key := types.NamespacedName{Name: name, Namespace: nsName}
	t.Cleanup(func() {
		stop()
		cur := &mcpv1alpha2.MCPServer{}
		if err := k8sClient.Get(ctx, key, cur); err != nil {
			return
		}
		cur.Finalizers = nil
		_ = k8sClient.Update(ctx, cur)
		_ = k8sClient.Delete(ctx, cur)
	})

	npKey := types.NamespacedName{Name: networkpolicy.NetworkPolicyName(name), Namespace: nsName}
	npExists := func() bool {
		err := k8sClient.Get(ctx, npKey, &networkingv1.NetworkPolicy{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("get NetworkPolicy: %v", err)
		}
		return err == nil
	}

	// Not governed: the unpinned server gets its allow-policy.
	require.Eventually(t, npExists, 15*time.Second, 100*time.Millisecond,
		"an unpinned server in an ungoverned namespace should get its allow-policy")

	// Governed: the allow-policy is withheld at once, not at the next poll.
	labelNamespace(t, nsName, true)
	assert.Eventually(t, func() bool { return !npExists() }, 15*time.Second, 100*time.Millisecond,
		"labelling the namespace enforce-egress should withhold the unpinned server's egress within seconds")

	// Un-governed again: restored on the same footing.
	labelNamespace(t, nsName, false)
	assert.Eventually(t, npExists, 15*time.Second, 100*time.Millisecond,
		"removing the enforce-egress label should restore the allow-policy within seconds")
}

func TestEnforceEgressLabelChanged_PassesOnlyThatLabel(t *testing.T) {
	p := enforceEgressLabelChanged()
	nsWith := func(labels map[string]string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: labels}}
	}
	on := map[string]string{networkpolicy.EnforceEgressLabel: "true"}

	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: nsWith(nil), ObjectNew: nsWith(on)}), "label added")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: nsWith(on), ObjectNew: nsWith(nil)}), "label removed")
	assert.False(t, p.Update(event.UpdateEvent{
		ObjectOld: nsWith(map[string]string{"team": "a"}),
		ObjectNew: nsWith(map[string]string{"team": "b"}),
	}), "an unrelated label change must not fan out to every server in the namespace")
	assert.False(t, p.Create(event.CreateEvent{Object: nsWith(on)}), "a new namespace holds no servers yet")
}

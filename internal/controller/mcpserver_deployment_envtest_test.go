package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// Against the API server, which defaults the fields the builder leaves empty:
// a steady reconcile must not rewrite the Deployment, and what another actor
// puts on it -- `kubectl rollout restart` stamps a template annotation -- must
// survive the operator's next apply (#221, as #220 did for policies).
func TestDeployment_SteadyAndForeignFieldsOnTheAPIServer(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dep-ssa"}}
	require.NoError(t, k8sClient.Create(ctx, ns))
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	server := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: ns.Name},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:1.36"},
	}
	require.NoError(t, k8sClient.Create(ctx, server))
	key := types.NamespacedName{Name: server.Name, Namespace: ns.Name}
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPServer{}
		if k8sClient.Get(ctx, key, cur) == nil {
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		}
	})
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme.Scheme, Recorder: &fakeEventRecorder{}}
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
	}
	depKey := types.NamespacedName{Name: "mcp-provider-srv", Namespace: ns.Name}
	dep := &appsv1.Deployment{}

	reconcile()
	require.NoError(t, k8sClient.Get(ctx, depKey, dep))
	first := dep.ResourceVersion
	reconcile()
	require.NoError(t, k8sClient.Get(ctx, depKey, dep))
	assert.Equal(t, first, dep.ResourceVersion, "a steady reconcile writes nothing")

	// kubectl rollout restart
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = "2026-10-12T12:00:00Z"
	require.NoError(t, k8sClient.Update(ctx, dep, client.FieldOwner("kubectl-rollout")))

	// A spec change makes the operator apply again.
	cur := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, key, cur))
	cur.Spec.Image = "busybox:1.37"
	require.NoError(t, k8sClient.Update(ctx, cur))
	reconcile()

	require.NoError(t, k8sClient.Get(ctx, depKey, dep))
	assert.Equal(t, "busybox:1.37", dep.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "2026-10-12T12:00:00Z", dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"],
		"a field another manager set survives the operator's apply")
}

func TestProviderPodToServer(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Labels: map[string]string{
		"mcp-hangar.io/provider": "srv", "app.kubernetes.io/managed-by": "mcp-hangar-operator",
	}}}
	reqs := providerPodToServer(ctx, pod)
	require.Len(t, reqs, 1)
	assert.Equal(t, types.NamespacedName{Namespace: "ns", Name: "srv"}, reqs[0].NamespacedName)

	pod.Labels["app.kubernetes.io/managed-by"] = "someone-else"
	assert.Empty(t, providerPodToServer(ctx, pod))
}

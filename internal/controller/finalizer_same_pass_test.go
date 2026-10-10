package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// Adding the finalizer used to end the reconcile with the deprecated
// Requeue: true, and for MCPServer that requeue was load-bearing: the
// finalizer write passes none of the controller's predicates, so nothing else
// would bring it back. The first reconcile now goes on to create the pod (#210).
func TestMCPServer_FirstReconcileAddsFinalizerAndCreatesThePod(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "one-pass", Namespace: "default", UID: "uid-one-pass"},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox@sha256:abc"},
	}
	r := newMCPServerReconciler(p)

	res := reconcileMCPServer(t, r, "one-pass", "default")

	assert.False(t, res.Requeue) //nolint:staticcheck // asserting the deprecated field is no longer used
	out := getMCPServer(t, r, "one-pass", "default")
	assert.Contains(t, out.Finalizers, finalizerName)
	require.NoError(t, r.Get(context.Background(),
		types.NamespacedName{Name: "mcp-provider-one-pass", Namespace: "default"}, &corev1.Pod{}),
		"the pod is created in the same pass")
}

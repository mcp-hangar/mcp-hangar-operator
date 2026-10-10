package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// An MCPServer runs one pod. The CRD used to accept replicas up to 10 and
// serve a scale subresource, so `kubectl scale --replicas=5` succeeded and
// changed nothing, and `replicas: 3` silently ran one pod (#192). replicas is
// now an on/off switch, and status.replicas says how many pods exist.
func TestCRDRules_ReplicasIsOnOrOff(t *testing.T) {
	two := int32(2)
	s := celServer("cel-replicas-two", mcpv1alpha2.MCPServerModeContainer)
	s.Spec.Replicas = &two
	requireRejected(t, createAndCleanup(t, s), "spec.replicas")

	for _, n := range []int32{0, 1} {
		ok := celServer("cel-replicas-"+string(rune('0'+n)), mcpv1alpha2.MCPServerModeContainer)
		ok.Spec.Replicas = &n
		require.NoError(t, createAndCleanup(t, ok))
	}
}

func TestCRDRules_NoScaleSubresource(t *testing.T) {
	s := celServer("cel-no-scale", mcpv1alpha2.MCPServerModeContainer)
	require.NoError(t, createAndCleanup(t, s))

	err := k8sClient.SubResource("scale").Get(ctx, s, &autoscalingv1.Scale{})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "kubectl scale must be refused, not accepted and ignored: %v", err)
}

func TestMCPServer_StatusReplicasCountsThePod(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "replicas-status", Namespace: "default", UID: "uid-192",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "replicas-status", "default")
	assert.Equal(t, int32(1), getMCPServer(t, r, "replicas-status", "default").Status.Replicas, "pod created")

	reconcileMCPServer(t, r, "replicas-status", "default")
	assert.Equal(t, int32(1), getMCPServer(t, r, "replicas-status", "default").Status.Replicas, "pod exists")

	zero := int32(0)
	cold := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "replicas-cold", Namespace: "default", UID: "uid-192c",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest", Replicas: &zero},
	}
	rc := newMCPServerReconciler(cold)
	reconcileMCPServer(t, rc, "replicas-cold", "default")
	assert.Equal(t, int32(0), getMCPServer(t, rc, "replicas-cold", "default").Status.Replicas)
}

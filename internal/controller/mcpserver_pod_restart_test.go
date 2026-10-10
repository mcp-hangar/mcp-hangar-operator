package controller

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
)

func restartServer(name string, generation int64) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("uid-" + name),
			Generation: generation, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
	}
}

func providerPod(server string, generation int64, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-" + server, Namespace: "default",
			Annotations: map[string]string{provider.AnnotationGeneration: strconv.FormatInt(generation, 10)},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: "busybox:latest"}}},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "provider", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
		}}},
	}
}

func podExists(t *testing.T, r *MCPServerReconciler, server string) bool {
	t.Helper()
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-" + server, Namespace: "default"}, &corev1.Pod{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get pod: %v", err)
	}
	return err == nil
}

func TestRestartBackoff_DoublesThenWaitsLongAtTheCap(t *testing.T) {
	assert.Equal(t, time.Duration(0), restartBackoff(0))
	assert.Equal(t, 10*time.Second, restartBackoff(1))
	assert.Equal(t, 20*time.Second, restartBackoff(2))
	assert.Equal(t, 80*time.Second, restartBackoff(4))
	assert.Equal(t, failedPodRetryAfter, restartBackoff(maxConsecutiveFailures))
	assert.Equal(t, failedPodRetryAfter, restartBackoff(maxConsecutiveFailures+3))
}

// Deleting the failed pod fired the watch and the next reconcile recreated it
// at once, so the returned backoff never applied; and after five failures the
// pod was never retried (#209).
func TestMCPServer_PodFailed_BackoffHoldsTheRestart(t *testing.T) {
	r := newMCPServerReconciler(restartServer("flaky", 1), providerPod("flaky", 1, corev1.PodFailed))

	reconcileMCPServer(t, r, "flaky", "default")
	assert.False(t, podExists(t, r, "flaky"), "the failed pod is deleted")

	res := reconcileMCPServer(t, r, "flaky", "default") // what the delete event triggers
	assert.False(t, podExists(t, r, "flaky"), "no new pod inside the backoff")
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	assert.LessOrEqual(t, res.RequeueAfter, 10*time.Second)

	// Once the backoff has passed, the pod comes back.
	s := getMCPServer(t, r, "flaky", "default")
	past := metav1.NewTime(time.Now().Add(-time.Minute))
	s.Status.LastStoppedAt = &past
	require.NoError(t, r.Status().Update(context.Background(), s))
	reconcileMCPServer(t, r, "flaky", "default")
	assert.True(t, podExists(t, r, "flaky"), "restarted after the backoff")
}

func TestMCPServer_PodFailed_CounterIsCappedAndRetriedLong(t *testing.T) {
	s := restartServer("capped", 1)
	s.Status.State = mcpv1alpha2.MCPServerStateDead
	s.Status.ConsecutiveFailures = maxConsecutiveFailures
	r := newMCPServerReconciler(s, providerPod("capped", 1, corev1.PodFailed))

	res := reconcileMCPServer(t, r, "capped", "default")

	out := getMCPServer(t, r, "capped", "default")
	assert.Equal(t, maxConsecutiveFailures, out.Status.ConsecutiveFailures, "capped, not 6")
	assert.False(t, podExists(t, r, "capped"))
	assert.Equal(t, failedPodRetryAfter, res.RequeueAfter, "retried, on the long interval")
}

func TestMCPServer_SpecChange_ResetsTheFailureCount(t *testing.T) {
	s := restartServer("respec", 2)
	s.Status.ConsecutiveFailures = maxConsecutiveFailures
	r := newMCPServerReconciler(s, providerPod("respec", 1, corev1.PodRunning)) // pod from generation 1

	reconcileMCPServer(t, r, "respec", "default")

	assert.Zero(t, getMCPServer(t, r, "respec", "default").Status.ConsecutiveFailures)
}

// A pod that exits 0 is restarted, and the server is not reported Cold, which
// means replicas: 0 (#209).
func TestMCPServer_PodExitedZero_IsRestartedNotCold(t *testing.T) {
	r := newMCPServerReconciler(restartServer("exits", 1), providerPod("exits", 1, corev1.PodSucceeded))

	reconcileMCPServer(t, r, "exits", "default")

	out := getMCPServer(t, r, "exits", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateInitializing, out.Status.State)
	assert.False(t, podExists(t, r, "exits"))
	reconcileMCPServer(t, r, "exits", "default")
	assert.True(t, podExists(t, r, "exits"), "recreated without a backoff")
}

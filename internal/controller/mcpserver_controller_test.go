package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// newMCPServerReconciler creates an MCPServerReconciler backed by a fake client.
func newMCPServerReconciler(objs ...runtime.Object) *MCPServerReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	return &MCPServerReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		Config:   DefaultReconcilerConfig(),
	}
}

func reconcileMCPServer(t *testing.T, r *MCPServerReconciler, name, namespace string) ctrl.Result {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	return result
}

func getMCPServer(t *testing.T, r *MCPServerReconciler, name, namespace string) *mcpv1alpha1.MCPServer {
	t.Helper()
	p := &mcpv1alpha1.MCPServer{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, p))
	return p
}

func TestMCPServer_ContainerMode_CreatesPod(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container", Namespace: "default", UID: "uid-1"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newMCPServerReconciler(p)

	// First reconcile: adds finalizer
	reconcileMCPServer(t, r, "test-container", "default")

	// Second reconcile: creates Pod
	reconcileMCPServer(t, r, "test-container", "default")

	// Verify Pod was created
	pod := &corev1.Pod{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-test-container", Namespace: "default"}, pod)
	require.NoError(t, err)
	assert.Equal(t, "busybox:latest", pod.Spec.Containers[0].Image)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

	// Verify owner reference
	require.Len(t, pod.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", pod.OwnerReferences[0].Kind)

	// Verify provider status
	result := getMCPServer(t, r, "test-container", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateInitializing, result.Status.State)
	assert.Equal(t, "mcp-provider-test-container", result.Status.PodName)
}

func TestMCPServer_ContainerMode_NoImage_MarksDead(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image", Namespace: "default", UID: "uid-2",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode: mcpv1alpha1.MCPServerModeContainer,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "no-image", "default")

	result := getMCPServer(t, r, "no-image", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateDead, result.Status.State)
	cond := result.Status.GetCondition(ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestMCPServer_ColdStart_ReplicasZero(t *testing.T) {
	replicas := int32(0)
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cold-provider", Namespace: "default", UID: "uid-3",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeContainer,
			Image:    "busybox:latest",
			Replicas: &replicas,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "cold-provider", "default")

	result := getMCPServer(t, r, "cold-provider", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateCold, result.Status.State)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)

	// Verify no Pod created
	pod := &corev1.Pod{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-cold-provider", Namespace: "default"}, pod)
	assert.Error(t, err, "Pod should not exist for cold provider")
}

func TestMCPServer_RemoteMode_NoEndpoint_MarksDead(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-no-ep", Namespace: "default", UID: "uid-4",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode: mcpv1alpha1.MCPServerModeRemote,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "remote-no-ep", "default")

	result := getMCPServer(t, r, "remote-no-ep", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateDead, result.Status.State)
}

func TestMCPServer_RemoteMode_WithEndpoint_AssumedReady(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-ok", Namespace: "default", UID: "uid-5",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "remote-ok", "default")

	result := getMCPServer(t, r, "remote-ok", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateReady, result.Status.State)
	assert.Equal(t, "http://example.com:8080", result.Status.Endpoint)
}

// hangarClientPointingAt builds a hangar.Client whose HealthCheckRemote calls
// hit the given test server URL, with retries disabled so tests stay fast.
func hangarClientPointingAt(url string) *hangar.Client {
	return hangar.NewClient(&hangar.Config{URL: url, MaxRetries: 1})
}

func TestMCPServer_RemoteMode_Unhealthy_RequeuesFast(t *testing.T) {
	// Server reports the endpoint as unhealthy (healthy=false, no error).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy": false}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-unhealthy", Namespace: "default", UID: "uid-fast-1",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	result := reconcileMCPServer(t, r, "remote-unhealthy", "default")

	// Degraded remotes must re-probe on the fast cadence so recovery is
	// detected quickly, not after the full readyRequeueAfter (5m) window.
	assert.Equal(t, errorRequeueAfter, result.RequeueAfter,
		"unhealthy remote should requeue on the fast (error) cadence")

	got := getMCPServer(t, r, "remote-unhealthy", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateDegraded, got.Status.State)
}

func TestMCPServer_RemoteMode_HealthCheckError_RequeuesFast(t *testing.T) {
	// Server returns an error payload -> HealthCheckRemote returns an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error": "connection refused"}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-err", Namespace: "default", UID: "uid-fast-2",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	result := reconcileMCPServer(t, r, "remote-err", "default")

	assert.Equal(t, errorRequeueAfter, result.RequeueAfter,
		"failed remote health check should requeue on the fast (error) cadence")

	got := getMCPServer(t, r, "remote-err", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateDegraded, got.Status.State)
}

func TestMCPServer_RemoteMode_Healthy_RequeuesSlow(t *testing.T) {
	// Healthy remote keeps the slow, steady-state cadence.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy": true, "tools": ["a", "b"]}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-healthy", Namespace: "default", UID: "uid-fast-3",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	result := reconcileMCPServer(t, r, "remote-healthy", "default")

	assert.Equal(t, readyRequeueAfter, result.RequeueAfter,
		"healthy remote should keep the slow (ready) cadence")

	got := getMCPServer(t, r, "remote-healthy", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateReady, got.Status.State)
}

func TestMCPServer_SpecDrift_RecreatesPod(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "drift-test", Namespace: "default", UID: "uid-6",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:1.0",
		},
	}
	r := newMCPServerReconciler(p)

	// First reconcile: creates Pod with generation=1 annotation
	reconcileMCPServer(t, r, "drift-test", "default")
	pod := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, pod))
	assert.Equal(t, "1", pod.Annotations[provider.AnnotationGeneration])

	// Simulate spec change: bump generation, change image
	pv := getMCPServer(t, r, "drift-test", "default")
	pv.Generation = 2
	pv.Spec.Image = "busybox:2.0"
	require.NoError(t, r.Update(context.Background(), pv))

	// Second reconcile: detects drift, deletes old Pod
	reconcileMCPServer(t, r, "drift-test", "default")

	// Verify old Pod was deleted
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, pod)
	assert.Error(t, err, "old Pod should be deleted after spec drift")

	// Third reconcile: creates new Pod with generation=2
	reconcileMCPServer(t, r, "drift-test", "default")

	newPod := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, newPod))
	assert.Equal(t, "busybox:2.0", newPod.Spec.Containers[0].Image)
	assert.Equal(t, "2", newPod.Annotations[provider.AnnotationGeneration])
}

func TestMCPServer_Deletion_CleansPodAndFinalizer(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "del-test", Namespace: "default", UID: "uid-7",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-provider-del-test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers:    []corev1.Container{{Name: "provider", Image: "busybox:latest"}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	r := newMCPServerReconciler(p, pod)

	// Call reconcileDelete directly (fake client cannot set DeletionTimestamp)
	fresh := getMCPServer(t, r, "del-test", "default")
	_, err := r.reconcileDelete(context.Background(), fresh)
	require.NoError(t, err)

	// Pod should be deleted
	err = r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-del-test", Namespace: "default"}, &corev1.Pod{})
	assert.Error(t, err, "Pod should be deleted during cleanup")

	// Finalizer should be removed
	updated := getMCPServer(t, r, "del-test", "default")
	assert.NotContains(t, updated.Finalizers, finalizerName)
}

func TestMCPServer_CapabilitiesPropagatedToStatus(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "caps-test", Namespace: "default", UID: "uid-8",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
			Capabilities: &mcpv1alpha1.MCPServerCapabilities{
				Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
					Egress: []mcpv1alpha1.EgressRuleSpec{
						{Host: "db.internal", Port: 5432, Protocol: "tcp", CIDR: "10.0.0.0/8"},
					},
				},
				EnforcementMode: "block",
			},
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "caps-test", "default")

	result := getMCPServer(t, r, "caps-test", "default")
	require.NotNil(t, result.Status.Capabilities)
	assert.Equal(t, "block", result.Status.Capabilities.EnforcementMode)
	require.NotNil(t, result.Status.Capabilities.Network)
	require.Len(t, result.Status.Capabilities.Network.Egress, 1)
	assert.Equal(t, "db.internal", result.Status.Capabilities.Network.Egress[0].Host)
}

func TestMCPServer_Finalizer_Added(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fin-test", Namespace: "default", UID: "uid-9"},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "fin-test", "default")

	result := getMCPServer(t, r, "fin-test", "default")
	assert.Contains(t, result.Finalizers, finalizerName)
}

func TestMCPServer_ObservedGeneration_Updated(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "obsgen-test", Namespace: "default", UID: "uid-10",
			Generation: 3, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:     mcpv1alpha1.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "obsgen-test", "default")

	result := getMCPServer(t, r, "obsgen-test", "default")
	assert.Equal(t, int64(3), result.Status.ObservedGeneration,
		fmt.Sprintf("observedGeneration (%d) should match generation (%d)", result.Status.ObservedGeneration, p.Generation))
}

func TestMCPServer_PodRunning_SetsReady(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "running-test", Namespace: "default", UID: "uid-11",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	// Create existing Pod in Running phase with all containers ready
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-running-test", Namespace: "default",
			Annotations: map[string]string{provider.AnnotationGeneration: strconv.FormatInt(1, 10)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "mcp-hangar.io/v1alpha1", Kind: "MCPServer",
				Name: "running-test", UID: "uid-11",
			}},
		},
		Spec: corev1.PodSpec{
			Containers:    []corev1.Container{{Name: "provider", Image: "busybox:latest"}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "provider", Ready: true},
			},
		},
	}
	r := newMCPServerReconciler(p, pod)

	reconcileMCPServer(t, r, "running-test", "default")

	result := getMCPServer(t, r, "running-test", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateReady, result.Status.State)
	assert.Equal(t, int32(1), result.Status.ReadyReplicas)
	assert.Equal(t, int32(0), result.Status.ConsecutiveFailures)

	cond := result.Status.GetCondition(ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestMCPServer_PodFailed_SetsDeadWithBackoff(t *testing.T) {
	p := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-test", Namespace: "default", UID: "uid-12",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPServerSpec{
			Mode:  mcpv1alpha1.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-failed-test", Namespace: "default",
			Annotations: map[string]string{provider.AnnotationGeneration: strconv.FormatInt(1, 10)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "mcp-hangar.io/v1alpha1", Kind: "MCPServer",
				Name: "failed-test", UID: "uid-12",
			}},
		},
		Spec: corev1.PodSpec{
			Containers:    []corev1.Container{{Name: "provider", Image: "busybox:latest"}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "provider", State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason: "OOMKilled", ExitCode: 137,
					},
				}},
			},
		},
	}
	r := newMCPServerReconciler(p, pod)

	reconcileMCPServer(t, r, "failed-test", "default")

	result := getMCPServer(t, r, "failed-test", "default")
	assert.Equal(t, mcpv1alpha1.MCPServerStateDead, result.Status.State)
	assert.Equal(t, int32(1), result.Status.ConsecutiveFailures)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)
}

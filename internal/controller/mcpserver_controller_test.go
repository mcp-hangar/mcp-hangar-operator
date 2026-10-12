package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// newMCPServerReconciler creates an MCPServerReconciler backed by a fake client.
func newMCPServerReconciler(objs ...runtime.Object) *MCPServerReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha2.AddToScheme(scheme)

	fakeClient := newFakeClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&mcpv1alpha2.MCPServer{}).
		Build()

	return &MCPServerReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
		// An observed enforcer, so NetworkPolicyApplied reads True when a
		// policy is written; the unenforced paths set their own (#199).
		EnforcementProbe: &networkpolicy.EnforcementProbe{Override: networkpolicy.EnforcementObserved},
	}
}

func reconcileMCPServer(t *testing.T, r *MCPServerReconciler, name, namespace string) ctrl.Result {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	return result
}

func getMCPServer(t *testing.T, r *MCPServerReconciler, name, namespace string) *mcpv1alpha2.MCPServer {
	t.Helper()
	p := &mcpv1alpha2.MCPServer{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, p))
	return p
}

func TestMCPServer_ContainerMode_CreatesDeployment(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container", Namespace: "default", UID: "uid-1"},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "test-container", "default")

	dep := &appsv1.Deployment{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-test-container", Namespace: "default"}, dep)
	require.NoError(t, err)
	assert.Equal(t, "busybox:latest", dep.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, corev1.RestartPolicyAlways, dep.Spec.Template.Spec.RestartPolicy)
	require.Len(t, dep.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", dep.OwnerReferences[0].Kind)
}

func TestMCPServer_ContainerMode_NoImage_MarksDead(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image", Namespace: "default", UID: "uid-2",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode: mcpv1alpha2.MCPServerModeContainer,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "no-image", "default")

	result := getMCPServer(t, r, "no-image", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDead, result.Status.State)
	cond := getCondition(result.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestMCPServer_ColdStart_ReplicasZero(t *testing.T) {
	replicas := int32(0)
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cold-provider", Namespace: "default", UID: "uid-3",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeContainer,
			Image:    "busybox:latest",
			Replicas: &replicas,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "cold-provider", "default")

	result := getMCPServer(t, r, "cold-provider", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateCold, result.Status.State)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)
	// Nothing scales a cold server up, so the condition must not promise it.
	cond := getCondition(result.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "Cold", cond.Reason)
	assert.NotContains(t, cond.Message, "on demand")
	assert.Contains(t, cond.Message, "spec.replicas")

	// Verify no Pod created
	pod := &corev1.Pod{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-cold-provider", Namespace: "default"}, pod)
	assert.Error(t, err, "Pod should not exist for cold provider")
}

func TestMCPServer_RemoteMode_NoEndpoint_MarksDead(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-no-ep", Namespace: "default", UID: "uid-4",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode: mcpv1alpha2.MCPServerModeRemote,
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "remote-no-ep", "default")

	result := getMCPServer(t, r, "remote-no-ep", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDead, result.Status.State)
}

func TestMCPServer_RemoteMode_WithEndpoint_AssumedReady(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-ok", Namespace: "default", UID: "uid-5",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "remote-ok", "default")

	result := getMCPServer(t, r, "remote-ok", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, result.Status.State)
	assert.Equal(t, "http://example.com:8080", result.Status.Endpoint)
}

// hangarClientPointingAt builds a hangar.Client whose health calls hit the
// given test server URL, with retries disabled so tests stay fast.
func hangarClientPointingAt(url string) *hangar.Client {
	return hangar.NewClient(&hangar.Config{URL: url, MaxRetries: 1})
}

func TestMCPServer_RemoteMode_Unhealthy_RequeuesFast(t *testing.T) {
	// Core answers, and reports the upstream as failing. Not an error: the
	// question was asked and answered -- the answer is bad news.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"consecutive_failures":3,"total_invocations":10,"total_failures":3,"success_rate":0.7,"can_retry":true}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-unhealthy", Namespace: "default", UID: "uid-fast-1",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
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
	assert.Equal(t, mcpv1alpha2.MCPServerStateDegraded, got.Status.State)
}

func TestMCPServer_RemoteMode_HealthCheckError_RequeuesFast(t *testing.T) {
	// Core itself cannot be reached -- a different failure from "the upstream
	// is unwell", and the controller keeps the two apart.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "connection refused"}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-err", Namespace: "default", UID: "uid-fast-2",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	result := reconcileMCPServer(t, r, "remote-err", "default")

	assert.Equal(t, errorRequeueAfter, result.RequeueAfter,
		"failed remote health check should requeue on the fast (error) cadence")

	got := getMCPServer(t, r, "remote-err", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDegraded, got.Status.State)
}

func TestMCPServer_RemoteMode_Healthy_RequeuesSlow(t *testing.T) {
	// Healthy remote keeps the slow, steady-state cadence.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy": true, "tools": ["a", "b"]}`))
	}))
	defer srv.Close()

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-healthy", Namespace: "default", UID: "uid-fast-3",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	result := reconcileMCPServer(t, r, "remote-healthy", "default")

	assert.Equal(t, readyRequeueAfter, result.RequeueAfter,
		"healthy remote should keep the slow (ready) cadence")

	got := getMCPServer(t, r, "remote-healthy", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)
}


func TestMCPServer_Deletion_CleansDeploymentAndFinalizer(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "del-test", Namespace: "default", UID: "uid-7",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	dep, err := provider.BuildDeploymentForMCPServer(p)
	require.NoError(t, err)
	r := newMCPServerReconciler(p, dep)

	// Call reconcileDelete directly (fake client cannot set DeletionTimestamp)
	fresh := getMCPServer(t, r, "del-test", "default")
	_, err = r.reconcileDelete(context.Background(), fresh)
	require.NoError(t, err)

	err = r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-del-test", Namespace: "default"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err), "the Deployment is deleted during cleanup")

	// Finalizer should be removed
	updated := getMCPServer(t, r, "del-test", "default")
	assert.NotContains(t, updated.Finalizers, finalizerName)
}

func TestMCPServer_CapabilitiesPropagatedToStatus(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "caps-test", Namespace: "default", UID: "uid-8",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
			Capabilities: &mcpv1alpha2.MCPServerCapabilities{
				Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
					Egress: []mcpv1alpha2.EgressRuleSpec{
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
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fin-test", Namespace: "default", UID: "uid-9"},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newMCPServerReconciler(p)

	reconcileMCPServer(t, r, "fin-test", "default")

	result := getMCPServer(t, r, "fin-test", "default")
	assert.Contains(t, result.Finalizers, finalizerName)
}

func TestMCPServer_ObservedGeneration_Updated(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "obsgen-test", Namespace: "default", UID: "uid-10",
			Generation: 3, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
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
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "running-test", Namespace: "default", UID: "uid-11",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	// Create existing Pod in Running phase with all containers ready
	pod := deploymentPod("running-test", corev1.PodStatus{
		Phase:             corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "provider", Ready: true}},
	})
	r := newMCPServerReconciler(p, pod)

	reconcileMCPServer(t, r, "running-test", "default")

	result := getMCPServer(t, r, "running-test", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, result.Status.State)
	assert.Equal(t, int32(1), result.Status.ReadyReplicas)
	assert.Equal(t, int32(0), result.Status.ConsecutiveFailures)

	cond := getCondition(result.Status.Conditions, ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestMCPServer_PodFailed_IsDead(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-test", Namespace: "default", UID: "uid-12",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	// Under a Deployment a pod fails only by eviction; the ReplicaSet replaces it.
	pod := deploymentPod("failed-test", corev1.PodStatus{
		Phase: corev1.PodFailed, Reason: "Evicted", Message: "low on memory",
	})
	r := newMCPServerReconciler(p, pod)

	reconcileMCPServer(t, r, "failed-test", "default")

	result := getMCPServer(t, r, "failed-test", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDead, result.Status.State)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)
	assert.Equal(t, "PodFailed", getCondition(result.Status.Conditions, ConditionReady).Reason)
}

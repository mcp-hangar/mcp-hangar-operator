package controller

import (
	"context"
	"fmt"
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
	"github.com/mcp-hangar/operator/pkg/provider"
)

// newProviderReconciler creates an MCPProviderReconciler backed by a fake client.
func newProviderReconciler(objs ...runtime.Object) *MCPProviderReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&mcpv1alpha1.MCPProvider{}).
		Build()

	return &MCPProviderReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		Config:   DefaultReconcilerConfig(),
	}
}

func reconcileProvider(t *testing.T, r *MCPProviderReconciler, name, namespace string) ctrl.Result {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	return result
}

func getProvider(t *testing.T, r *MCPProviderReconciler, name, namespace string) *mcpv1alpha1.MCPProvider {
	t.Helper()
	p := &mcpv1alpha1.MCPProvider{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, p))
	return p
}

func TestMCPProvider_ContainerMode_CreatesPod(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container", Namespace: "default", UID: "uid-1"},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newProviderReconciler(p)

	// First reconcile: adds finalizer
	reconcileProvider(t, r, "test-container", "default")

	// Second reconcile: creates Pod
	reconcileProvider(t, r, "test-container", "default")

	// Verify Pod was created
	pod := &corev1.Pod{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-test-container", Namespace: "default"}, pod)
	require.NoError(t, err)
	assert.Equal(t, "busybox:latest", pod.Spec.Containers[0].Image)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

	// Verify owner reference
	require.Len(t, pod.OwnerReferences, 1)
	assert.Equal(t, "MCPProvider", pod.OwnerReferences[0].Kind)

	// Verify provider status
	result := getProvider(t, r, "test-container", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateInitializing, result.Status.State)
	assert.Equal(t, "mcp-provider-test-container", result.Status.PodName)
}

func TestMCPProvider_ContainerMode_NoImage_MarksDead(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image", Namespace: "default", UID: "uid-2",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode: mcpv1alpha1.ProviderModeContainer,
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "no-image", "default")

	result := getProvider(t, r, "no-image", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateDead, result.Status.State)
	cond := result.Status.GetCondition(ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestMCPProvider_ColdStart_ReplicasZero(t *testing.T) {
	replicas := int32(0)
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "cold-provider", Namespace: "default", UID: "uid-3",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:     mcpv1alpha1.ProviderModeContainer,
			Image:    "busybox:latest",
			Replicas: &replicas,
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "cold-provider", "default")

	result := getProvider(t, r, "cold-provider", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateCold, result.Status.State)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)

	// Verify no Pod created
	pod := &corev1.Pod{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-cold-provider", Namespace: "default"}, pod)
	assert.Error(t, err, "Pod should not exist for cold provider")
}

func TestMCPProvider_RemoteMode_NoEndpoint_MarksDead(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-no-ep", Namespace: "default", UID: "uid-4",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode: mcpv1alpha1.ProviderModeRemote,
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "remote-no-ep", "default")

	result := getProvider(t, r, "remote-no-ep", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateDead, result.Status.State)
}

func TestMCPProvider_RemoteMode_WithEndpoint_AssumedReady(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-ok", Namespace: "default", UID: "uid-5",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:     mcpv1alpha1.ProviderModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "remote-ok", "default")

	result := getProvider(t, r, "remote-ok", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateReady, result.Status.State)
	assert.Equal(t, "http://example.com:8080", result.Status.Endpoint)
}

func TestMCPProvider_SpecDrift_RecreatesPod(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "drift-test", Namespace: "default", UID: "uid-6",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
			Image: "busybox:1.0",
		},
	}
	r := newProviderReconciler(p)

	// First reconcile: creates Pod with generation=1 annotation
	reconcileProvider(t, r, "drift-test", "default")
	pod := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, pod))
	assert.Equal(t, "1", pod.Annotations[provider.AnnotationGeneration])

	// Simulate spec change: bump generation, change image
	pv := getProvider(t, r, "drift-test", "default")
	pv.Generation = 2
	pv.Spec.Image = "busybox:2.0"
	require.NoError(t, r.Update(context.Background(), pv))

	// Second reconcile: detects drift, deletes old Pod
	reconcileProvider(t, r, "drift-test", "default")

	// Verify old Pod was deleted
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, pod)
	assert.Error(t, err, "old Pod should be deleted after spec drift")

	// Third reconcile: creates new Pod with generation=2
	reconcileProvider(t, r, "drift-test", "default")

	newPod := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-drift-test", Namespace: "default"}, newPod))
	assert.Equal(t, "busybox:2.0", newPod.Spec.Containers[0].Image)
	assert.Equal(t, "2", newPod.Annotations[provider.AnnotationGeneration])
}

func TestMCPProvider_Deletion_CleansPodAndFinalizer(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name: "del-test", Namespace: "default", UID: "uid-7",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
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
	r := newProviderReconciler(p, pod)

	// Call reconcileDelete directly (fake client cannot set DeletionTimestamp)
	fresh := getProvider(t, r, "del-test", "default")
	_, err := r.reconcileDelete(context.Background(), fresh)
	require.NoError(t, err)

	// Pod should be deleted
	err = r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-del-test", Namespace: "default"}, &corev1.Pod{})
	assert.Error(t, err, "Pod should be deleted during cleanup")

	// Finalizer should be removed
	updated := getProvider(t, r, "del-test", "default")
	assert.NotContains(t, updated.Finalizers, finalizerName)
}

func TestMCPProvider_CapabilitiesPropagatedToStatus(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "caps-test", Namespace: "default", UID: "uid-8",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:     mcpv1alpha1.ProviderModeRemote,
			Endpoint: "http://example.com:8080",
			Capabilities: &mcpv1alpha1.ProviderCapabilities{
				Network: &mcpv1alpha1.NetworkCapabilitiesSpec{
					Egress: []mcpv1alpha1.EgressRuleSpec{
						{Host: "db.internal", Port: 5432, Protocol: "tcp", CIDR: "10.0.0.0/8"},
					},
				},
				EnforcementMode: "block",
			},
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "caps-test", "default")

	result := getProvider(t, r, "caps-test", "default")
	require.NotNil(t, result.Status.Capabilities)
	assert.Equal(t, "block", result.Status.Capabilities.EnforcementMode)
	require.NotNil(t, result.Status.Capabilities.Network)
	require.Len(t, result.Status.Capabilities.Network.Egress, 1)
	assert.Equal(t, "db.internal", result.Status.Capabilities.Network.Egress[0].Host)
}

func TestMCPProvider_Finalizer_Added(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "fin-test", Namespace: "default", UID: "uid-9"},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
			Image: "busybox:latest",
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "fin-test", "default")

	result := getProvider(t, r, "fin-test", "default")
	assert.Contains(t, result.Finalizers, finalizerName)
}

func TestMCPProvider_ObservedGeneration_Updated(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "obsgen-test", Namespace: "default", UID: "uid-10",
			Generation: 3, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:     mcpv1alpha1.ProviderModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newProviderReconciler(p)

	reconcileProvider(t, r, "obsgen-test", "default")

	result := getProvider(t, r, "obsgen-test", "default")
	assert.Equal(t, int64(3), result.Status.ObservedGeneration,
		fmt.Sprintf("observedGeneration (%d) should match generation (%d)", result.Status.ObservedGeneration, p.Generation))
}

func TestMCPProvider_PodRunning_SetsReady(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "running-test", Namespace: "default", UID: "uid-11",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
			Image: "busybox:latest",
		},
	}
	// Create existing Pod in Running phase with all containers ready
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-running-test", Namespace: "default",
			Annotations: map[string]string{provider.AnnotationGeneration: strconv.FormatInt(1, 10)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "mcp-hangar.io/v1alpha1", Kind: "MCPProvider",
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
	r := newProviderReconciler(p, pod)

	reconcileProvider(t, r, "running-test", "default")

	result := getProvider(t, r, "running-test", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateReady, result.Status.State)
	assert.Equal(t, int32(1), result.Status.ReadyReplicas)
	assert.Equal(t, int32(0), result.Status.ConsecutiveFailures)

	cond := result.Status.GetCondition(ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestMCPProvider_PodFailed_SetsDeadWithBackoff(t *testing.T) {
	p := &mcpv1alpha1.MCPProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-test", Namespace: "default", UID: "uid-12",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha1.MCPProviderSpec{
			Mode:  mcpv1alpha1.ProviderModeContainer,
			Image: "busybox:latest",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-failed-test", Namespace: "default",
			Annotations: map[string]string{provider.AnnotationGeneration: strconv.FormatInt(1, 10)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "mcp-hangar.io/v1alpha1", Kind: "MCPProvider",
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
	r := newProviderReconciler(p, pod)

	reconcileProvider(t, r, "failed-test", "default")

	result := getProvider(t, r, "failed-test", "default")
	assert.Equal(t, mcpv1alpha1.ProviderStateDead, result.Status.State)
	assert.Equal(t, int32(1), result.Status.ConsecutiveFailures)
	assert.Equal(t, int32(0), result.Status.ReadyReplicas)
}

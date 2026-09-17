package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// The acceptance criterion for a status is that an apiserver ACCEPTS it, not
// that the in-memory object looks right afterwards.
//
// Every other test of the ready path runs against the fake client, which does
// not validate the CRD schema. So they all passed while the real write failed:
// the success path cleared Degraded with an empty reason, metav1.Condition
// .Reason is MinLength=1, and the apiserver rejected the ENTIRE status
// subresource write -- Ready, Available, State, Tools, ToolsCount,
// LastHealthCheck and all. Live effect: every MCPServer reported Initializing
// indefinitely while its pod was Running and Ready, and every reconcile of a
// healthy server returned an error and requeued into the same failure (#174).
//
// Hence envtest: a real apiserver with this repo's generated CRDs, driven
// through the same Reconcile the manager calls.
func TestMCPServer_ContainerMode_HealthyPod_StatusIsAcceptedByTheAPIServer(t *testing.T) {
	const name = "ready-174"
	const nsName = "mcp-ready-174"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	// Core answers with a tool list, so the ready branch has a Tools/ToolsCount
	// to publish -- the fields the rejected write was dropping on the floor.
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": []string{"alpha", "beta"}})
	}))
	t.Cleanup(core.Close)

	server := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsName},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox:latest",
		},
	}
	require.NoError(t, k8sClient.Create(ctx, server))

	key := types.NamespacedName{Name: name, Namespace: nsName}
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPServer{}
		if err := k8sClient.Get(ctx, key, cur); err != nil {
			return
		}
		cur.Finalizers = nil
		_ = k8sClient.Update(ctx, cur)
		_ = k8sClient.Delete(ctx, cur)
	})

	// The MCPServer reconciler is not registered in this suite
	// (enableMCPServerReconciler is false), so reconciles are driven by hand
	// here and nothing races with them.
	r := &MCPServerReconciler{
		Client:       k8sClient,
		Scheme:       scheme.Scheme,
		Recorder:     &fakeEventRecorder{},
		HangarClient: hangarClientPointingAt(core.URL),
	}
	req := ctrl.Request{NamespacedName: key}

	// First pass adds the finalizer, second creates the Pod.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	podKey := types.NamespacedName{Name: "mcp-provider-" + name, Namespace: nsName}
	pod := &corev1.Pod{}
	require.NoError(t, k8sClient.Get(ctx, podKey, pod),
		"the second reconcile should have created the provider Pod")

	// envtest runs no kubelet, so publish the status a kubelet would: the pod
	// is Running and every container is Ready. This is the live situation the
	// bug was found in -- pod healthy, CR insisting otherwise.
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
	}
	pod.Status.ContainerStatuses = nil
	for _, c := range pod.Spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:    c.Name,
			Image:   c.Image,
			Ready:   true,
			Started: boolPtr(true),
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
			},
		})
	}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))

	// The pass that takes the ready branch, and writes the status that used to
	// be rejected whole.
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err,
		"reconciling a healthy MCPServer must not error: a single empty condition "+
			"reason makes the apiserver reject the entire status subresource write")
	assert.Equal(t, readyRequeueAfter, result.RequeueAfter,
		"a ready server should settle onto the slow cadence")

	// Read back from the API, not from the in-memory object the reconcile held:
	// what the apiserver persisted is the whole question here.
	got := &mcpv1alpha2.MCPServer{}
	require.NoError(t, k8sClient.Get(ctx, key, got))

	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State,
		"kubectl get mcpserver must show Ready, not Initializing")
	assert.Equal(t, int32(1), got.Status.ReadyReplicas)
	assert.Equal(t, int32(0), got.Status.ConsecutiveFailures)
	assert.NotNil(t, got.Status.LastHealthCheck)

	ready := getCondition(got.Status.Conditions, ConditionReady)
	require.NotNil(t, ready, "Ready condition was never persisted")
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, "ProviderReady", ready.Reason)

	available := getCondition(got.Status.Conditions, ConditionAvailable)
	require.NotNil(t, available, "Available condition was never persisted")
	assert.Equal(t, metav1.ConditionTrue, available.Status)

	// The condition whose empty reason sank the write. It must be cleared, and
	// cleared with a reason the schema accepts.
	degraded := getCondition(got.Status.Conditions, ConditionDegraded)
	require.NotNil(t, degraded, "Degraded condition was never persisted")
	assert.Equal(t, metav1.ConditionFalse, degraded.Status)
	assert.NotEmpty(t, degraded.Reason,
		"metav1.Condition.Reason is MinLength=1; an empty one invalidates the whole status")

	// The tool inventory the controller fetched was being discarded with the
	// rest of the rejected write.
	assert.Equal(t, int32(2), got.Status.ToolsCount)
	assert.Equal(t, []string{"alpha", "beta"}, got.Status.Tools)

	// Every persisted condition, not just the ones asserted above, has to
	// satisfy the schema -- otherwise the next write to this status fails.
	for _, c := range got.Status.Conditions {
		assert.NotEmpty(t, c.Reason, "condition %q persisted with an empty reason", c.Type)
	}
}

package controller

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// #409: a server that had been Ready=True kept Ready=True when it degraded.
// State and Degraded (or Progressing) moved, Ready did not, so the Ready print
// column and `kubectl wait --for=condition=Ready` reported a failing server as
// serving. Each case reconciles to Ready=True, switches the input, reconciles
// again, and asserts Ready=False under the branch's reason.

// assertNotReady checks the server left Ready, on both State and condition.
func assertNotReady(t *testing.T, r *MCPServerReconciler, name, reason string) {
	t.Helper()
	got := getMCPServer(t, r, name, "default")
	assert.NotEqual(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)
	ready := getCondition(got.Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status, "Ready follows State away from Ready")
	assert.Equal(t, reason, ready.Reason)
}

func requireReady(t *testing.T, r *MCPServerReconciler, name, reason string) {
	t.Helper()
	got := getMCPServer(t, r, name, "default")
	require.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)
	ready := getCondition(got.Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	require.Equal(t, metav1.ConditionTrue, ready.Status)
	require.Equal(t, reason, ready.Reason)
}

func warnings(rec *events.FakeRecorder) int {
	n := 0
	for {
		select {
		case e := <-rec.Events:
			if len(e) >= len(corev1.EventTypeWarning) && e[:len(corev1.EventTypeWarning)] == corev1.EventTypeWarning {
				n++
			}
		default:
			return n
		}
	}
}

func TestMCPServer_RemoteMode_DegradedClearsReady(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		reason string
	}{
		"core rejects the credentials": {http.StatusUnauthorized, ``, ReasonCoreAuthRejected},
		"core cannot be read":          {http.StatusInternalServerError, `{"error":"boom"}`, "HealthCheckFailed"},
		"core reports failures": {http.StatusOK,
			`{"consecutive_failures":3,"total_invocations":10,"total_failures":3,"success_rate":0.7,"can_retry":true}`,
			"EndpointUnhealthy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var degraded atomic.Bool
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !degraded.Load() {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(core.Close)

			p := remoteCR("default", "remote", "http://example.com:8080")
			r := newMCPServerReconciler(p)
			r.HangarClient = hangarClientPointingAt(core.URL)
			rec := events.NewFakeRecorder(50)
			r.Recorder = rec

			reconcileMCPServer(t, r, "remote", "default")
			requireReady(t, r, "remote", "EndpointHealthy")
			_ = warnings(rec)

			degraded.Store(true)
			reconcileMCPServer(t, r, "remote", "default")
			assertNotReady(t, r, "remote", c.reason)
			reconcileMCPServer(t, r, "remote", "default")
			assertNotReady(t, r, "remote", c.reason)
			assert.Equal(t, 1, warnings(rec), "one Warning on the transition, not one per reconcile")
		})
	}
}

func TestMCPServer_ContainerMode_DegradedClearsReady(t *testing.T) {
	notReadyContainer := corev1.PodStatus{
		Phase:             corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "provider", Ready: false}},
	}
	cases := map[string]struct {
		pod       *corev1.PodStatus // the pod's new status; nil deletes it
		toolsFail bool
		reason    string
	}{
		"pod pending":         {pod: &corev1.PodStatus{Phase: corev1.PodPending}, reason: "PodPending"},
		"container not ready": {pod: &notReadyContainer, reason: "ContainersStarting"},
		"tools read fails":    {toolsFail: true, reason: "ToolsFetchFailed"},
		"pod gone":            {reason: "PodCreating"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var toolsFail atomic.Bool
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if toolsFail.Load() {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"tools":[]}`))
			}))
			t.Cleanup(core.Close)

			p := &mcpv1alpha2.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "box", Namespace: "default", UID: "uid-409",
					Generation: 1, Finalizers: []string{finalizerName}},
				Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
			}
			pod := deploymentPod("box", corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "provider", Ready: true}},
			})
			r := newMCPServerReconciler(p, pod)
			r.HangarClient = hangarClientPointingAt(core.URL)

			reconcileMCPServer(t, r, "box", "default")
			requireReady(t, r, "box", "ProviderReady")

			switch {
			case c.toolsFail:
				toolsFail.Store(true)
			case c.pod == nil:
				require.NoError(t, r.Delete(t.Context(), pod))
			default:
				current := &corev1.Pod{}
				require.NoError(t, r.Get(t.Context(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, current))
				current.Status = *c.pod
				require.NoError(t, r.Status().Update(t.Context(), current))
			}

			reconcileMCPServer(t, r, "box", "default")
			assertNotReady(t, r, "box", c.reason)
		})
	}
}

// A crash-looping container passes through Running-but-not-Ready between
// back-offs. That step now writes Ready=False/ContainersStarting, and the
// crash Warning must not take it for a new failure on every cycle (#210).
func TestMCPServer_CrashLoop_ThroughContainersStarting_WarnsOnce(t *testing.T) {
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "loop", Namespace: "default", UID: "uid-409-loop",
			Generation: 1, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
	}
	pod := deploymentPod("loop", crashLooping(3))
	r := newMCPServerReconciler(p, pod)
	rec := events.NewFakeRecorder(50)
	r.Recorder = rec

	setPod := func(status corev1.PodStatus) {
		current := &corev1.Pod{}
		require.NoError(t, r.Get(t.Context(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, current))
		current.Status = status
		require.NoError(t, r.Status().Update(t.Context(), current))
	}
	restarting := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "provider", RestartCount: 3, Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}}

	reconcileMCPServer(t, r, "loop", "default")
	stoppedAt := getMCPServer(t, r, "loop", "default").Status.LastStoppedAt
	require.NotNil(t, stoppedAt)
	setPod(restarting)
	reconcileMCPServer(t, r, "loop", "default")
	assertNotReady(t, r, "loop", "ContainersStarting")
	setPod(crashLooping(4))
	reconcileMCPServer(t, r, "loop", "default")

	assert.Equal(t, 1, warnings(rec), "one Warning for the crash loop, not one per cycle")
	assert.Equal(t, stoppedAt, getMCPServer(t, r, "loop", "default").Status.LastStoppedAt,
		"LastStoppedAt marks when the loop began")
}

// A registration that stops completing moves State to Degraded, and Ready
// goes with it. Here core loses the server and refuses to take it back.
func TestMCPServer_RegistrationFailure_ClearsReady(t *testing.T) {
	core := newCore()
	r, rec := overHTTPS(core.serve(t), remoteCR("default", "remote", "https://upstream.svc.example:8443/mcp"))

	reconcileMCPServer(t, r, "remote", "default")
	requireReady(t, r, "remote", "EndpointHealthy")
	_ = warnings(rec)

	core.mu.Lock()
	delete(core.servers, "remote")
	core.registerRefusal = `{"error":{"code":"ValidationError","message":"refused"}}`
	core.mu.Unlock()
	reconcileMCPServer(t, r, "remote", "default")
	assertNotReady(t, r, "remote", ReasonRegistrationFailed)
	reconcileMCPServer(t, r, "remote", "default")
	assertNotReady(t, r, "remote", ReasonRegistrationFailed)
	assert.Equal(t, 1, warnings(rec), "one Warning on the transition, not one per reconcile")
}

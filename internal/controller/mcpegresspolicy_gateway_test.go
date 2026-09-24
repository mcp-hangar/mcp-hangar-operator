package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
)

func gatewayTestPod(name, ns string, podLabels map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "gateway", Image: "busybox:latest"}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: status},
		}},
	}
}

var gatewayLabels = map[string]string{"app.kubernetes.io/name": "mcp-hangar"}

func TestGatewayPodBecameReady(t *testing.T) {
	sel, err := labels.Parse(DefaultGatewayPodSelector)
	require.NoError(t, err)
	p := gatewayPodBecameReady(sel)

	gwNotReady := gatewayTestPod("gw", "hangar", gatewayLabels, false)
	gwReady := gatewayTestPod("gw", "hangar", gatewayLabels, true)
	other := map[string]string{"app.kubernetes.io/name": "something-else"}
	otherNotReady := gatewayTestPod("x", "hangar", other, false)
	otherReady := gatewayTestPod("x", "hangar", other, true)
	noCondition := gatewayTestPod("gw", "hangar", gatewayLabels, false)
	noCondition.Status.Conditions = nil

	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"gateway turns Ready", p.Update(event.UpdateEvent{ObjectOld: gwNotReady, ObjectNew: gwReady}), true},
		{"gateway turns Ready from no condition", p.Update(event.UpdateEvent{ObjectOld: noCondition, ObjectNew: gwReady}), true},
		{"gateway stays Ready", p.Update(event.UpdateEvent{ObjectOld: gwReady, ObjectNew: gwReady}), false},
		{"gateway goes unready", p.Update(event.UpdateEvent{ObjectOld: gwReady, ObjectNew: gwNotReady}), false},
		{"unrelated pod turns Ready", p.Update(event.UpdateEvent{ObjectOld: otherNotReady, ObjectNew: otherReady}), false},
		{"Ready gateway first seen", p.Create(event.CreateEvent{Object: gwReady}), true},
		{"unready gateway first seen", p.Create(event.CreateEvent{Object: gwNotReady}), false},
		{"Ready unrelated pod first seen", p.Create(event.CreateEvent{Object: otherReady}), false},
		{"gateway deleted", p.Delete(event.DeleteEvent{Object: gwReady}), false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.got, c.name)
	}
}

func TestPoliciesForGatewayPod_EnqueuesEveryPolicy(t *testing.T) {
	r := newEgressReconciler(testPolicy("a", "team-a"), testPolicy("b", "team-b"))
	reqs := r.policiesForGatewayPod(context.Background(), gatewayTestPod("gw", "hangar", gatewayLabels, true))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: "a", Namespace: "team-a"}},
		{NamespacedName: types.NamespacedName{Name: "b", Namespace: "team-b"}},
	}, reqs)
}

// pushCounter is a stand-in core that counts L7 policy pushes.
type pushCounter struct {
	mu    sync.Mutex
	count int
}

func (c *pushCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// waitStable waits until the push count has not moved for quiet, and returns it.
func (c *pushCounter) waitStable(t *testing.T, quiet, limit time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(limit)
	last, since := c.get(), time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if n := c.get(); n != last {
			last, since = n, time.Now()
			continue
		}
		if time.Since(since) >= quiet {
			return last
		}
	}
	t.Fatalf("push count never settled (last %d)", last)
	return last
}

func setPodReady(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))
}

// The #1306 shape end to end, against a real apiserver and a running
// controller: the policy has been delivered and nothing about it changes, then
// a gateway pod becomes Ready -- as it does after a restart that lost the
// policy. The operator must push again promptly, and must not for a pod that is
// not the gateway.
func TestEgressPolicy_GatewayPodReady_RedeliversL7Policy(t *testing.T) {
	const nsName = "l7-redeliver"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	pushes := &pushCounter{}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			pushes.mu.Lock()
			pushes.count++
			pushes.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"l7_policy_set":true,"persisted":false}`))
	}))
	t.Cleanup(core.Close)

	sel, err := labels.Parse(DefaultGatewayPodSelector)
	require.NoError(t, err)

	// A manager of its own: the suite's shared one does not register this
	// controller, and this one needs a HangarClient and the gateway watch.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, (&MCPEgressPolicyReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		Recorder:           mgr.GetEventRecorder("mcpegresspolicy-controller"),
		HangarClient:       hangar.NewClient(&hangar.Config{URL: core.URL, MaxRetries: 0}),
		GatewayPodSelector: sel,
	}).SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() { _ = mgr.Start(mgrCtx) }()

	require.NoError(t, k8sClient.Create(ctx, testServer("srv", nsName)))
	policy := testPolicy("pol", nsName)
	require.NoError(t, k8sClient.Create(ctx, policy))
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPEgressPolicy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "pol", Namespace: nsName}, cur); err == nil {
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		}
	})

	// Initial delivery, then quiet: nothing about the policy changes from here.
	settled := pushes.waitStable(t, 2*time.Second, 30*time.Second)
	require.GreaterOrEqual(t, settled, 1, "the policy was never delivered in the first place")

	// A pod that is not the gateway becoming Ready is not a reason to push.
	other := gatewayTestPod("unrelated", nsName, map[string]string{"app": "unrelated"}, false)
	other.Status = corev1.PodStatus{}
	require.NoError(t, k8sClient.Create(ctx, other))
	setPodReady(t, other)
	time.Sleep(2 * time.Second)
	assert.Equal(t, settled, pushes.get(), "an unrelated pod turning Ready re-delivered the policy")

	// The gateway pod comes up (envtest runs no kubelet, so publish the Ready
	// condition a kubelet would) and the policy is pushed again, in seconds.
	gw := gatewayTestPod("mcp-hangar-0", nsName, gatewayLabels, false)
	gw.Status = corev1.PodStatus{}
	require.NoError(t, k8sClient.Create(ctx, gw))
	setPodReady(t, gw)

	deadline := time.Now().Add(10 * time.Second)
	for pushes.get() == settled && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	assert.Greater(t, pushes.get(), settled, "a gateway pod turning Ready did not re-deliver the L7 policy")
}

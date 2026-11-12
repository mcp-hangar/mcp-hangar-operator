package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
)

// fakeGatewayPod is one gateway pod with an in-memory L7 store, as core keeps
// it with no persistence backend.
type fakeGatewayPod struct {
	mu         sync.Mutex
	registered bool // whether this core holds the server yet
	lossy      bool // accepts a push and keeps nothing
	held       map[string]bool
	hosts      map[string]bool
	srv        *httptest.Server
}

func newFakeGatewayPod(t *testing.T, registered bool) *fakeGatewayPod {
	p := &fakeGatewayPod{registered: registered, held: map[string]bool{}, hosts: map[string]bool{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeGatewayPod) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts[r.Host] = true
	if !strings.HasSuffix(r.URL.Path, "/l7_policy") {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/mcp_servers/"), "/l7_policy")
	if !p.registered {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"mcp_server_not_found"}`))
		return
	}
	switch r.Method {
	case http.MethodPost:
		p.held[id] = !p.lossy
		_, _ = w.Write([]byte(`{"l7_policy_set":true,"persisted":false}`))
	case http.MethodGet:
		if !p.held[id] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no_l7_policy"}`))
			return
		}
		_, _ = w.Write([]byte(`{"defaultAction":"Deny","mode":"Enforce"}`))
	}
}

func (p *fakeGatewayPod) holds(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[id]
}

func (p *fakeGatewayPod) register() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registered = true
}

func (p *fakeGatewayPod) sawHost(h string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hosts[h]
}

func (p *fakeGatewayPod) port(t *testing.T) int32 {
	u, err := url.Parse(p.srv.URL)
	require.NoError(t, err)
	n, err := strconv.ParseInt(u.Port(), 10, 32)
	require.NoError(t, err)
	return int32(n)
}

// startGatewayPod creates a gateway pod whose http port is fake's listener and
// publishes the Ready status and address a kubelet would (envtest runs none).
func startGatewayPod(t *testing.T, name, ns string, fake *fakeGatewayPod) *corev1.Pod {
	t.Helper()
	pod := gatewayTestPod(name, ns, gatewayLabels, false)
	pod.Status = corev1.PodStatus{}
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: fake.port(t)}}
	require.NoError(t, k8sClient.Create(ctx, pod))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)) })
	pod.Status.PodIP = "127.0.0.1"
	pod.Status.PodIPs = []corev1.PodIP{{IP: "127.0.0.1"}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))
	return pod
}

// #405: a single-replica gateway rolled with surge 1 and unavailable 0. The
// surge pod turns Ready while the Service still routes to the old pod, which
// holds the policy and is about to go. The operator must deliver to the pod
// that turned Ready and read it back there; until that pod holds the policy,
// L7Delivered is False naming it, not a DeliveredNotPersisted claim.
func TestEgressPolicy_RollingUpdate_ConfirmsL7OnReadyPod(t *testing.T) {
	const nsName = "l7-rollout"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	oldCore := newFakeGatewayPod(t, true)
	// The new core does not hold the server until the MCPServer reconciler
	// registers it again, so a push to it is refused at first.
	newCore := newFakeGatewayPod(t, false)
	// The Service: it routes to the old pod for the whole test.
	svc := httptest.NewServer(http.HandlerFunc(oldCore.serve))
	t.Cleanup(svc.Close)
	svcHost := strings.TrimPrefix(svc.URL, "http://")

	sel, err := labels.Parse(DefaultGatewayPodSelector)
	require.NoError(t, err)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, (&MCPEgressPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("mcpegresspolicy-controller"),
		// The Service URL here is a loopback address, which names no
		// namespace; a cluster one does (<svc>.<ns>.svc).
		HangarClient:       hangar.NewClient(&hangar.Config{URL: svc.URL, MaxRetries: 1, GatewayNamespace: nsName}),
		GatewayPodSelector: sel,
	}).SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() { _ = mgr.Start(mgrCtx) }()

	require.NoError(t, k8sClient.Create(ctx, testServer("srv", nsName)))
	require.NoError(t, k8sClient.Create(ctx, testPolicy("pol", nsName)))
	t.Cleanup(func() {
		cur := &mcpv1alpha2.MCPEgressPolicy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "pol", Namespace: nsName}, cur); err == nil {
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		}
	})
	l7 := func() (*metav1.Condition, *metav1.Condition) {
		cur := &mcpv1alpha2.MCPEgressPolicy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "pol", Namespace: nsName}, cur); err != nil {
			return nil, nil
		}
		return meta.FindStatusCondition(cur.Status.Conditions, EgressPolicyConditionL7Delivered),
			meta.FindStatusCondition(cur.Status.Conditions, EgressPolicyConditionDegraded)
	}

	// The old gateway pod is up and holds the policy.
	startGatewayPod(t, "mcp-hangar-old", nsName, oldCore)
	require.Eventually(t, func() bool { return oldCore.holds("srv") }, 30*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		c, _ := l7()
		return c != nil && c.Status == metav1.ConditionTrue
	}, 30*time.Second, 100*time.Millisecond, "the first delivery was never reported")

	// The surge pod turns Ready, holding nothing; the Service still picks the
	// old pod. The status must stop claiming delivery and name the new pod.
	startGatewayPod(t, "mcp-hangar-new", nsName, newCore)
	assert.Eventually(t, func() bool {
		c, d := l7()
		return c != nil && c.Status == metav1.ConditionFalse &&
			strings.Contains(c.Message, "mcp-hangar-new") && d != nil && d.Status == metav1.ConditionTrue
	}, 15*time.Second, 100*time.Millisecond,
		"while the pod that turned Ready holds no policy, L7Delivered must be False naming it and the policy Degraded")
	c, _ := l7()
	if c != nil {
		t.Logf("while the new pod holds nothing: L7Delivered=%s reason=%s msg=%q", c.Status, c.Reason, c.Message)
	}

	// The new core registers the server; the retry delivers to that pod by
	// its address, with the Service host, while the Service still routes to
	// the old pod.
	newCore.register()
	require.Eventually(t, func() bool { return newCore.holds("srv") }, 30*time.Second, 100*time.Millisecond,
		"the pod that turned Ready never received the policy")
	assert.True(t, newCore.sawHost(svcHost), "the per-pod request must keep the Service host")
	assert.Eventually(t, func() bool {
		c, _ := l7()
		return c != nil && c.Status == metav1.ConditionTrue && c.Reason == l7ReasonDeliveredNotPersisted &&
			strings.Contains(c.Message, "mcp-hangar-new")
	}, 15*time.Second, 100*time.Millisecond, "L7Delivered did not return to True after the read-back")
	c, _ = l7()
	if c != nil {
		t.Logf("after the read-back: L7Delivered=%s reason=%s msg=%q", c.Status, c.Reason, c.Message)
	}
}

// A pod that acknowledges the push and still answers the read-back with no
// policy is not delivered to, whatever the push said: L7Delivered is False with
// GatewayPodMissingPolicy naming the pod, and the policy is retried soon
// rather than at the informer resync. The failing pod sorts first and the
// healthy one still gets the policy in the same pass. A pod with the gateway
// label in another namespace is not core's and is sent nothing: it would be
// handed the operator's API key.
func TestEgressPolicy_ReadBackMissing_IsNotDelivered(t *testing.T) {
	good := newFakeGatewayPod(t, true)
	lossy := newFakeGatewayPod(t, true)
	lossy.mu.Lock()
	lossy.lossy = true
	lossy.mu.Unlock()
	impostor := newFakeGatewayPod(t, true)
	pod := func(name, ns string, fake *fakeGatewayPod) *corev1.Pod {
		p := gatewayTestPod(name, ns, gatewayLabels, true)
		p.Spec.Containers[0].Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: fake.port(t)}}
		p.Status.PodIP = "127.0.0.1"
		return p
	}
	p := testPolicy("pol", "default")
	p.Finalizers = []string{l7PolicyFinalizer}
	r := newEgressReconciler(testServer("srv", "default"), p,
		pod("gw-a", "hangar", lossy), pod("gw-b", "hangar", good), pod("gw-x", "tenant", impostor))
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: good.srv.URL, MaxRetries: 1, GatewayNamespace: "hangar"})
	sel, err := labels.Parse(DefaultGatewayPodSelector)
	require.NoError(t, err)
	r.GatewayPodSelector = sel

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "pol", Namespace: "default"}})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, res.RequeueAfter)
	assert.True(t, good.holds("srv"), "a failing pod must not keep the policy from the next one")
	impostor.mu.Lock()
	assert.Empty(t, impostor.hosts, "a gateway-labelled pod outside core's namespace was sent a request")
	impostor.mu.Unlock()

	out := &mcpv1alpha2.MCPEgressPolicy{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "pol", Namespace: "default"}, out))
	c := condStatus(out, EgressPolicyConditionL7Delivered)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "GatewayPodMissingPolicy", c.Reason)
	assert.Contains(t, c.Message, "hangar/gw-a")
	assert.NotContains(t, c.Message, "hangar/gw-b")
	assert.Equal(t, metav1.ConditionTrue, condStatus(out, EgressPolicyConditionDegraded).Status)
}

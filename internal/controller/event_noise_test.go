package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func countContaining(evs []string, s string) int {
	n := 0
	for _, e := range evs {
		if strings.Contains(e, s) {
			n++
		}
	}
	return n
}

// An unhealthy remote server put a Warning in the event stream on every 10 s
// re-probe (#210). It now warns when it becomes unhealthy, and once more when
// it recovers.
func TestMCPServer_RemoteUnhealthy_WarnsOncePerTransition(t *testing.T) {
	healthy := false
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if healthy {
			_, _ = w.Write([]byte(`{"consecutive_failures": 0}`))
			return
		}
		_, _ = w.Write([]byte(`{"consecutive_failures": 3, "success_rate": 0.1}`))
	}))
	t.Cleanup(core.Close)

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "noisy", Namespace: "default", UID: "uid-noisy", Finalizers: []string{finalizerName}},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "http://example.com:8080"},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(core.URL)
	rec := events.NewFakeRecorder(50)
	r.Recorder = rec

	for range 4 {
		reconcileMCPServer(t, r, "noisy", "default")
	}
	evs := drain(rec)
	assert.Equal(t, 1, countContaining(evs, "Remote endpoint unhealthy"), "%v", evs)

	healthy = true
	for range 3 {
		reconcileMCPServer(t, r, "noisy", "default")
	}
	evs = drain(rec)
	assert.Equal(t, 1, countContaining(evs, "Remote endpoint is healthy"), "%v", evs)
}

// Discovery emitted SyncStarted and SyncCompleted on every refresh (#210).
func TestDiscovery_SyncEventsOnlyWhenTheOutcomeChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha2.AddToScheme(scheme))
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"},
		Data:       map[string]string{"providers.yaml": oneProviderYAML},
	}
	src := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "default", Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type: mcpv1alpha2.DiscoveryTypeConfigMap, Mode: mcpv1alpha2.DiscoveryModeAdditive,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "cm"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, src).
		WithStatusSubresource(&mcpv1alpha2.MCPDiscoverySource{}, &mcpv1alpha2.MCPServer{}).Build()
	rec := events.NewFakeRecorder(50)
	r := &MCPDiscoverySourceReconciler{Client: c, Scheme: scheme, Recorder: rec}

	for range 3 {
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "src", Namespace: "default"}})
		require.NoError(t, err)
	}
	evs := drain(rec)
	assert.Zero(t, countContaining(evs, "SyncStarted"), "%v", evs)
	assert.Equal(t, 1, countContaining(evs, "SyncCompleted"), "%v", evs)
}

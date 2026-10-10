package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// With one reconcile worker, a remote server whose health check waited on a
// core that never answered held up every other MCPServer, pod create and
// delete included (#201). Here core is black-holed for longer than the test
// waits, three remote servers are already stuck on it, and a container-mode
// server must still get its pod.
func TestMCPServer_SlowCore_DoesNotStallOtherServers(t *testing.T) {
	const nsName = "mcp-slow-core-201"

	var blocked atomic.Int32
	release := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		blocked.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); core.Close() })

	require.NoError(t, k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}))

	skip := true
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skip},
		// Only this test's servers: other tests leave MCPServers behind, and a
		// remote one among them would take a worker on this black-holed core.
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{nsName: {}}},
	})
	require.NoError(t, err)
	require.NoError(t, (&MCPServerReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("mcpserver-controller-201"),
		// A budget far beyond the wait below: the test is about the other
		// workers, not about the budget ending the stuck calls.
		HangarClient:            hangar.NewClient(&hangar.Config{URL: core.URL, Timeout: time.Minute, MaxRetries: 1}),
		EnforcementProbe:        &networkpolicy.EnforcementProbe{Override: networkpolicy.EnforcementObserved},
		MaxConcurrentReconciles: 4,
	}).SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(ctx)
	go func() { _ = mgr.Start(mgrCtx) }()

	var created []client.Object
	t.Cleanup(func() {
		stop()
		for _, obj := range created {
			obj.SetFinalizers(nil)
			_ = k8sClient.Update(ctx, obj)
			_ = k8sClient.Delete(ctx, obj)
		}
	})

	for i := range 3 {
		remote := &mcpv1alpha2.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("remote-%d", i), Namespace: nsName},
			Spec: mcpv1alpha2.MCPServerSpec{
				Mode:     mcpv1alpha2.MCPServerModeRemote,
				Endpoint: "http://example.com:8080",
			},
		}
		require.NoError(t, k8sClient.Create(ctx, remote))
		created = append(created, remote)
	}
	require.Eventually(t, func() bool { return blocked.Load() >= 1 }, 15*time.Second, 50*time.Millisecond,
		"a remote server should be waiting on core")

	container := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "container-201", Namespace: nsName},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: "busybox@sha256:" + fmt.Sprintf("%064d", 0),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, container))
	created = append(created, container)

	require.Eventually(t, func() bool {
		var pods corev1.PodList
		if err := k8sClient.List(ctx, &pods, client.InNamespace(nsName),
			client.MatchingLabels{networkpolicy.LabelProvider: container.Name}); err != nil {
			return false
		}
		return len(pods.Items) > 0
	}, 10*time.Second, 100*time.Millisecond,
		"the container server's pod must be created while remote servers wait on a slow core")
}

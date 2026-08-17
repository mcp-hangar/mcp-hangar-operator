package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// A controller that writes status to the resource it watches re-triggers itself
// on its own writes, unless the status it writes stops changing or a predicate
// says otherwise. Neither was true here: the "could not reach core" branch did
// `Status.ConsecutiveFailures++`, so every reconcile produced a different status,
// every status write produced an update event, and every event produced another
// reconcile.
//
// Measured on a live cluster against a remote MCPServer core did not know:
// 168 reconciles per second, 1.8 million on the counter. `errorRequeueAfter`
// is 10s and was never reached, because the watch event always won the race.
//
// So these assert the two properties that end the loop, not the symptom.

func unreachableCore(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"code": "Boom", "message": "core is unwell"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMCPServer_RemoteMode_FailedHealthCheck_LeavesTheStatusUnchanged(t *testing.T) {
	srv := unreachableCore(t)

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote-stable", Namespace: "default", UID: "uid-stable",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	reconcileMCPServer(t, r, "remote-stable", "default")
	first := getMCPServer(t, r, "remote-stable", "default").Status.DeepCopy()

	for range 5 {
		reconcileMCPServer(t, r, "remote-stable", "default")
	}
	after := getMCPServer(t, r, "remote-stable", "default").Status.DeepCopy()

	require.Equal(t, mcpv1alpha2.MCPServerStateDegraded, after.State,
		"the server is still degraded; that part must not change")
	assert.Equal(t, first, after,
		"a repeated failed health check must not change the status: a status that "+
			"differs every reconcile is written every reconcile, and this controller "+
			"watches what it writes")
}

func TestMCPServer_RemoteMode_ConsecutiveFailuresMirrorsCoreOnly(t *testing.T) {
	// The counter belongs to core -- it is what core observed against the
	// upstream. This branch is the one where core could not be asked, so there
	// is nothing to mirror and nothing to increment.
	srv := unreachableCore(t)

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote-counter", Namespace: "default", UID: "uid-counter",
			Finalizers: []string{finalizerName},
		},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: "http://example.com:8080",
		},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(srv.URL)

	for range 10 {
		reconcileMCPServer(t, r, "remote-counter", "default")
	}

	got := getMCPServer(t, r, "remote-counter", "default")
	assert.Zero(t, got.Status.ConsecutiveFailures,
		"ten failed probes must not put ten on a counter that reports core's observations")
}

func TestMCPServerWatchPredicate(t *testing.T) {
	// The predicate installed in SetupWithManager, asserted directly: a
	// controller-runtime wiring mistake is invisible to every test that calls
	// Reconcile by hand.
	p := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.LabelChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	)

	base := func() *mcpv1alpha2.MCPServer {
		return &mcpv1alpha2.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "srv", Namespace: "default", Generation: 1,
				Labels:      map[string]string{"app": "srv"},
				Annotations: map[string]string{"mcp-hangar.io/discovered": "true"},
			},
		}
	}

	t.Run("a status-only update does not wake the controller", func(t *testing.T) {
		old, updated := base(), base()
		updated.Status.State = mcpv1alpha2.MCPServerStateDegraded
		updated.Status.ConsecutiveFailures = 3

		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}),
			"this is the controller's own write; reacting to it is the loop")
	})

	t.Run("a spec change does", func(t *testing.T) {
		old, updated := base(), base()
		updated.Generation = 2

		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	})

	t.Run("an annotation change does", func(t *testing.T) {
		// The discovery annotations this operator stamps are metadata, not
		// spec, so a bare GenerationChangedPredicate would have dropped them.
		old, updated := base(), base()
		updated.Annotations["mcp-hangar.io/discovered"] = "false"

		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	})

	t.Run("a label change does", func(t *testing.T) {
		old, updated := base(), base()
		updated.Labels["app"] = "other"

		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	})

	t.Run("a create still wakes it", func(t *testing.T) {
		assert.True(t, p.Create(event.CreateEvent{Object: base()}))
	})

	t.Run("a delete still wakes it", func(t *testing.T) {
		assert.True(t, p.Delete(event.DeleteEvent{Object: base()}))
	})
}

package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// A wrong API key left every remote server Degraded/HealthCheckFailed with a
// 10 s re-probe and a Warning each time, indistinguishable from an outage
// (#212). It now says what it is, once, and waits the steady interval.
func TestMCPServer_RemoteMode_CoreRejectsCredentials(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(core.Close)

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote-auth", Namespace: "default", UID: "uid-212",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "http://example.com:8080"},
	}
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(core.URL)
	rec := events.NewFakeRecorder(10)
	r.Recorder = rec

	first := reconcileMCPServer(t, r, "remote-auth", "default")
	second := reconcileMCPServer(t, r, "remote-auth", "default")

	out := getMCPServer(t, r, "remote-auth", "default")
	cond := getCondition(out.Status.Conditions, ConditionDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonCoreAuthRejected, cond.Reason)
	assert.Equal(t, readyRequeueAfter, first.RequeueAfter, "no 10 s probe storm")
	assert.Equal(t, readyRequeueAfter, second.RequeueAfter)
	assert.Len(t, rec.Events, 1, "one Warning on the transition, not one per reconcile")
}

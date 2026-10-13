package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// fakeCore records the DELETEs it receives, so a test can tell whether a CR
// deletion reached core.
type fakeCore struct {
	mu      sync.Mutex
	deletes []string
}

func (f *fakeCore) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.deletes = append(f.deletes, r.URL.Path)
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCore) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletes...)
}

func deleteMCPServerAgainst(t *testing.T, core *httptest.Server, p *mcpv1alpha2.MCPServer) *events.FakeRecorder {
	t.Helper()
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(core.URL)
	rec := events.NewFakeRecorder(10)
	r.Recorder = rec
	fresh := getMCPServer(t, r, p.Name, p.Namespace)
	_, err := r.reconcileDelete(context.Background(), fresh)
	require.NoError(t, err)
	assert.NotContains(t, getMCPServer(t, r, p.Name, p.Namespace).Finalizers, finalizerName)
	return rec
}

// A remote CR mirrors a server core already has from its config; the operator
// never registered it. Deleting the mirror used to delete the real server in
// core until the next reload (#202).
func TestMCPServer_Delete_RemoteMirrorLeavesCoreAlone(t *testing.T) {
	fc := &fakeCore{}
	core := fc.server(t)
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "default", UID: "uid-202a",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "http://backend:8080"},
	}

	rec := deleteMCPServerAgainst(t, core, p)

	assert.Empty(t, fc.deleted(), "core must not be asked to delete a server the operator did not register")
	close(rec.Events)
	var reasons []string
	for e := range rec.Events {
		reasons = append(reasons, e)
	}
	assert.Contains(t, strings.Join(reasons, "\n"), ReasonDeregistrationSkipped)
}

// The annotation #202 read is no longer what makes a server the CR's: a CR
// anyone can edit could claim another namespace's server with it (#164).
func TestMCPServer_Delete_TheOldAnnotationIsNotAClaim(t *testing.T) {
	fc := &fakeCore{}
	core := fc.server(t)
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default", UID: "uid-202b",
			Finalizers:  []string{finalizerName},
			Annotations: map[string]string{"mcp-hangar.io/registered-by": "operator"}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "http://owned:8080"},
	}

	deleteMCPServerAgainst(t, core, p)

	assert.Empty(t, fc.deleted())
}

// A container-mode server is in core because of the pod this operator ran, so
// deleting the CR removes it from core.
func TestMCPServer_Delete_ContainerModeIsDeregistered(t *testing.T) {
	fc := &fakeCore{}
	core := fc.server(t)
	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "box", Namespace: "default", UID: "uid-202c",
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
	}

	deleteMCPServerAgainst(t, core, p)

	assert.Equal(t, []string{"/api/mcp_servers/box"}, fc.deleted())
}

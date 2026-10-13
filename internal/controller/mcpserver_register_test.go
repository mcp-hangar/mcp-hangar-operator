package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// registry is a core that holds servers by name: GET .../health answers 404
// for one it does not hold, POST /api/mcp_servers adds one, DELETE removes one.
type registry struct {
	mu        sync.Mutex
	servers   map[string]string // name -> endpoint
	calls     []string
	refuseSSR bool
	// forbidPost answers a POST 403, as core does to a role without
	// mcp_servers:write.
	forbidPost bool
	// onPost runs as a POST arrives, before it is answered.
	onPost func(name string)
}

func (c *registry) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api/mcp_servers")
		c.calls = append(c.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if c.onPost != nil {
				c.onPost(body["mcp_server_id"])
			}
			if c.forbidPost {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"AccessDeniedError"}}`))
				return
			}
			if c.refuseSSR {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"ssrf_blocked"}`))
				return
			}
			if _, ok := c.servers[body["mcp_server_id"]]; ok {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"code":"ValidationError","message":"McpServer already exists: x"}}`))
				return
			}
			c.servers[body["mcp_server_id"]] = body["endpoint"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
		case r.Method == http.MethodDelete:
			name := strings.TrimPrefix(path, "/")
			if _, ok := c.servers[name]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(c.servers, name)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/health"):
			if _, ok := c.servers[strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/health")]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"consecutive_failures":0,"total_invocations":0,"total_failures":0,"success_rate":1,"can_retry":true}`))
		case strings.HasSuffix(path, "/tools"):
			_, _ = w.Write([]byte(`{"tools":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *registry) snapshot() (map[string]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	servers := map[string]string{}
	for k, v := range c.servers {
		servers[k] = v
	}
	return servers, append([]string(nil), c.calls...)
}

func remoteCR(name, endpoint string, annotations map[string]string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("uid-" + name),
			Finalizers: []string{finalizerName}, Annotations: annotations},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: endpoint},
	}
}

func reconcilerAgainst(core *httptest.Server, p *mcpv1alpha2.MCPServer) (*MCPServerReconciler, *events.FakeRecorder) {
	r := newMCPServerReconciler(p)
	r.HangarClient = hangarClientPointingAt(core.URL)
	rec := events.NewFakeRecorder(20)
	r.Recorder = rec
	return r, rec
}

// A remote CR naming a server core does not hold puts it there, marked as the
// operator's, and deleting the CR takes it out (#164).
func TestRemote_UnknownServerIsRegisteredMarkedAndDeregistered(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	p := remoteCR("notes", "http://notes.providers.svc.cluster.local:8080/mcp", nil)
	r, _ := reconcilerAgainst(core, p)

	// The mark is on the CR before core is asked: the order that cannot leave a
	// server in core that deleting the CR would never remove.
	c.onPost = func(name string) {
		got := getMCPServer(t, r, name, "default")
		assert.Equal(t, RegisteredByOperator, got.Annotations[AnnotationRegisteredBy], "marked before the POST")
	}

	result := reconcileMCPServer(t, r, "notes", "default")
	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": p.Spec.Endpoint}, servers)
	got := getMCPServer(t, r, "notes", "default")
	assert.Equal(t, RegisteredByOperator, got.Annotations[AnnotationRegisteredBy])
	assert.Equal(t, p.Spec.Endpoint, got.Annotations[AnnotationRegisteredEndpoint])
	progressing := apimeta.FindStatusCondition(got.Status.Conditions, ConditionProgressing)
	require.NotNil(t, progressing)
	assert.Equal(t, ReasonRegistered, progressing.Reason)
	assert.Equal(t, errorRequeueAfter, result.RequeueAfter, "re-probe soon to see it healthy")

	reconcileMCPServer(t, r, "notes", "default")
	got = getMCPServer(t, r, "notes", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)

	_, err := r.reconcileDelete(context.Background(), got)
	require.NoError(t, err)
	servers, _ = c.snapshot()
	assert.Empty(t, servers, "deleting the CR deregisters what the operator registered")
}

// A CR naming a server core already holds -- one its config declares -- stays
// a mirror: nothing is registered, and nothing marks it as the operator's.
func TestRemote_ServerCoreHoldsStaysAMirror(t *testing.T) {
	c := &registry{servers: map[string]string{"backend": "http://backend:8080/mcp"}}
	core := c.server(t)
	p := remoteCR("backend", "http://backend:8080/mcp", nil)
	r, _ := reconcilerAgainst(core, p)

	reconcileMCPServer(t, r, "backend", "default")

	_, calls := c.snapshot()
	for _, call := range calls {
		assert.NotContains(t, call, "POST", "core already holds the server")
	}
	got := getMCPServer(t, r, "backend", "default")
	assert.NotContains(t, got.Annotations, AnnotationRegisteredBy)
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)
}

// Core refusing a private endpoint it does not trust is a setting to change in
// core: the condition names it, the Warning fires once, and the CR is not
// re-asked every 10 seconds.
func TestRemote_UntrustedEndpointNamesTheCoreSetting(t *testing.T) {
	c := &registry{servers: map[string]string{}, refuseSSR: true}
	core := c.server(t)
	p := remoteCR("internal", "http://10.244.1.7:8080/mcp", nil)
	r, rec := reconcilerAgainst(core, p)

	result := reconcileMCPServer(t, r, "internal", "default")
	reconcileMCPServer(t, r, "internal", "default")

	got := getMCPServer(t, r, "internal", "default")
	degraded := apimeta.FindStatusCondition(got.Status.Conditions, ConditionDegraded)
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonEndpointNotTrusted, degraded.Reason)
	assert.Contains(t, degraded.Message, "ssrf.trusted_networks")
	assert.Equal(t, readyRequeueAfter, result.RequeueAfter)

	close(rec.Events)
	warnings := 0
	for e := range rec.Events {
		if strings.Contains(e, ReasonEndpointNotTrusted) {
			warnings++
		}
	}
	assert.Equal(t, 1, warnings, "one Warning on the transition, not one per reconcile")
}

// Marked, and the process died before the POST: the next reconcile registers
// it, without needing the mark written again.
func TestRemote_MarkedButNotRegisteredIsRegistered(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	endpoint := "http://notes:8080/mcp"
	p := remoteCR("notes", endpoint, map[string]string{
		AnnotationRegisteredBy: RegisteredByOperator, AnnotationRegisteredEndpoint: endpoint})
	r, _ := reconcilerAgainst(core, p)

	reconcileMCPServer(t, r, "notes", "default")

	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": endpoint}, servers)
}

// Core cannot change an endpoint: a changed spec.endpoint deregisters the
// server and registers it again, and the mark follows.
func TestRemote_ChangedEndpointIsRegisteredAgain(t *testing.T) {
	c := &registry{servers: map[string]string{"notes": "http://old:8080/mcp"}}
	core := c.server(t)
	p := remoteCR("notes", "http://new:8080/mcp", map[string]string{
		AnnotationRegisteredBy: RegisteredByOperator, AnnotationRegisteredEndpoint: "http://old:8080/mcp"})
	r, _ := reconcilerAgainst(core, p)

	reconcileMCPServer(t, r, "notes", "default")

	servers, calls := c.snapshot()
	assert.Equal(t, map[string]string{"notes": "http://new:8080/mcp"}, servers)
	joined := strings.Join(calls, "\n")
	assert.Less(t, strings.Index(joined, "DELETE"), strings.Index(joined, "POST"), "removed before it is added again")
	assert.Equal(t, "http://new:8080/mcp", getMCPServer(t, r, "notes", "default").Annotations[AnnotationRegisteredEndpoint])
}

// A mirror whose endpoint differs from what core holds is not the operator's
// to change: no annotation, no delete.
func TestRemote_MirrorWithAnotherEndpointIsLeftAlone(t *testing.T) {
	c := &registry{servers: map[string]string{"backend": "http://backend:8080/mcp"}}
	core := c.server(t)
	p := remoteCR("backend", "http://elsewhere:8080/mcp", nil)
	r, _ := reconcilerAgainst(core, p)

	reconcileMCPServer(t, r, "backend", "default")

	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"backend": "http://backend:8080/mcp"}, servers)
}

// A key whose role cannot create servers -- provider-admin -- gets a condition
// that says so, not "check the API key", which is valid.
func TestRemote_RoleWithoutWriteSaysSo(t *testing.T) {
	c := &registry{servers: map[string]string{}, forbidPost: true}
	core := c.server(t)
	p := remoteCR("notes", "http://notes:8080/mcp", nil)
	r, _ := reconcilerAgainst(core, p)

	result := reconcileMCPServer(t, r, "notes", "default")

	degraded := apimeta.FindStatusCondition(getMCPServer(t, r, "notes", "default").Status.Conditions, ConditionDegraded)
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonRegistrationForbidden, degraded.Reason)
	assert.Contains(t, degraded.Message, "mcp_servers:write")
	assert.Equal(t, readyRequeueAfter, result.RequeueAfter)
}

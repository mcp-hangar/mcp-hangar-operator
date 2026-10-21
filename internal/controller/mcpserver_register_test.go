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
	coordinationv1 "k8s.io/api/coordination/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	// failStart answers a start 500, as core does when it cannot connect.
	failStart bool
	started   map[string]bool
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
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			if c.failStart {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"connection refused"}`))
				return
			}
			if c.started == nil {
				c.started = map[string]bool{}
			}
			c.started[strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/start")] = true
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && path == "/":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if c.onPost != nil {
				c.mu.Unlock()
				c.onPost(body["mcp_server_id"])
				c.mu.Lock()
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

func remoteCR(namespace, name, endpoint string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + namespace + "-" + name),
			Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: endpoint},
	}
}

const operatorNamespace = "mcp-hangar"

func reconcilerAgainst(core *httptest.Server, objs ...runtime.Object) (*MCPServerReconciler, *events.FakeRecorder) {
	r := newMCPServerReconciler(objs...)
	r.HangarClient = hangarClientPointingAt(core.URL)
	r.Claims = &RegistrationClaims{Client: r.Client, Reader: r.Client, Namespace: operatorNamespace}
	rec := events.NewFakeRecorder(50)
	r.Recorder = rec
	return r, rec
}

func claimOf(t *testing.T, r *MCPServerReconciler, name string) *coordinationv1.Lease {
	t.Helper()
	lease, err := r.Claims.get(context.Background(), name)
	require.NoError(t, err)
	return lease
}

func degradedReason(t *testing.T, r *MCPServerReconciler, namespace, name string) *metav1.Condition {
	t.Helper()
	return apimeta.FindStatusCondition(getMCPServer(t, r, name, namespace).Status.Conditions, ConditionDegraded)
}

// A remote CR naming a server core does not hold puts it there, claimed in the
// operator's namespace, has core start it, and deleting the CR takes it out
// and gives the name back (#164).
func TestRemote_UnknownServerIsRegisteredStartedAndDeregistered(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://notes.providers.svc.cluster.local:8080/mcp")
	r, _ := reconcilerAgainst(core, p)

	// The claim exists before core is asked: the order that cannot leave a
	// server in core that deleting the CR would never remove.
	c.onPost = func(name string) {
		assert.True(t, holds(claimOf(t, r, name), p), "claimed before the POST")
	}

	reconcileMCPServer(t, r, "notes", "default")

	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": p.Spec.Endpoint}, servers)
	assert.True(t, c.started["notes"], "core was asked to start it")
	claim := claimOf(t, r, "notes")
	require.NotNil(t, claim)
	assert.Equal(t, "default/notes/uid-default-notes", *claim.Spec.HolderIdentity)
	assert.Equal(t, p.Spec.Endpoint, claim.Annotations[claimEndpointAnnotation])
	assert.Equal(t, "true", claim.Annotations[claimStartedAnnotation])
	got := getMCPServer(t, r, "notes", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, got.Status.State)
	assert.Empty(t, got.Annotations, "nothing on the CR decides ownership")

	require.NoError(t, r.deregisterOwned(context.Background(), got))
	servers, _ = c.snapshot()
	assert.Empty(t, servers, "deleting the CR deregisters what it registered")
	assert.Nil(t, claimOf(t, r, "notes"), "and gives the name back")
}

// Core reports a server nobody has called as healthy. Until a start succeeds,
// a mistyped endpoint must not read Ready.
func TestRemote_RegisteredButNotStartedIsNotReady(t *testing.T) {
	c := &registry{servers: map[string]string{}, failStart: true}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://notes.provders.svc.cluster.local:8080/mcp")
	r, _ := reconcilerAgainst(core, p)

	result := reconcileMCPServer(t, r, "notes", "default")
	reconcileMCPServer(t, r, "notes", "default")

	got := getMCPServer(t, r, "notes", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDegraded, got.Status.State)
	degraded := degradedReason(t, r, "default", "notes")
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonStartFailed, degraded.Reason)
	assert.Nil(t, apimeta.FindStatusCondition(got.Status.Conditions, ConditionReady))
	assert.Equal(t, errorRequeueAfter, result.RequeueAfter, "the upstream's problem: retried soon")

	c.mu.Lock()
	c.failStart = false
	c.mu.Unlock()
	reconcileMCPServer(t, r, "notes", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, getMCPServer(t, r, "notes", "default").Status.State)
}

// A CR naming a server core already holds -- one its config declares -- stays
// a mirror: nothing is registered or claimed, and deleting it leaves core alone.
func TestRemote_ServerCoreHoldsStaysAMirror(t *testing.T) {
	c := &registry{servers: map[string]string{"backend": "http://backend:8080/mcp"}}
	core := c.server(t)
	p := remoteCR("default", "backend", "http://elsewhere:8080/mcp")
	r, _ := reconcilerAgainst(core, p)

	reconcileMCPServer(t, r, "backend", "default")

	_, calls := c.snapshot()
	for _, call := range calls {
		assert.NotContains(t, call, "POST", "core already holds the server")
	}
	assert.Nil(t, claimOf(t, r, "backend"))
	assert.Equal(t, mcpv1alpha2.MCPServerStateReady, getMCPServer(t, r, "backend", "default").Status.State)

	require.NoError(t, r.deregisterOwned(context.Background(), getMCPServer(t, r, "backend", "default")))
	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"backend": "http://backend:8080/mcp"}, servers)
}

// Core keys servers by name across the cluster. Two namespaces naming the same
// server: the first registers it, the second is told the name is taken, and
// deleting the second leaves the first's server in core.
func TestRemote_SameNameInAnotherNamespaceIsTaken(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	a := remoteCR("team-a", "notes", "http://notes.team-a.svc:8080/mcp")
	b := remoteCR("team-b", "notes", "http://notes.team-b.svc:8080/mcp")
	r, _ := reconcilerAgainst(core, a, b)

	reconcileMCPServer(t, r, "notes", "team-a")
	reconcileMCPServer(t, r, "notes", "team-b")

	degraded := degradedReason(t, r, "team-b", "notes")
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonNameTaken, degraded.Reason)
	assert.Contains(t, degraded.Message, "team-a/notes")
	assert.NotEqual(t, mcpv1alpha2.MCPServerStateReady, getMCPServer(t, r, "notes", "team-b").Status.State,
		"b must not report a's server as its own")

	require.NoError(t, r.deregisterOwned(context.Background(), getMCPServer(t, r, "notes", "team-b")))
	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": a.Spec.Endpoint}, servers, "b's delete leaves a's server")
}

// Two namespaces racing for one name: exactly one POST reaches core.
func TestRemote_TheClaimDecidesARace(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	a := remoteCR("team-a", "notes", "http://notes.team-a.svc:8080/mcp")
	b := remoteCR("team-b", "notes", "http://notes.team-b.svc:8080/mcp")
	r, _ := reconcilerAgainst(core, a, b)

	// b's reconcile happens while a's POST is in flight: both saw 404.
	c.onPost = func(string) {
		c.onPost = nil
		reconcileMCPServer(t, r, "notes", "team-b")
	}
	reconcileMCPServer(t, r, "notes", "team-a")

	_, calls := c.snapshot()
	posts := 0
	for _, call := range calls {
		if call == "POST /api/mcp_servers/" {
			posts++
		}
	}
	assert.Equal(t, 1, posts)
	assert.True(t, holds(claimOf(t, r, "notes"), a))
}

// Editing a CR cannot make another namespace's server its own: the annotation
// #202 trusted is ignored, and the endpoint is not re-pointed.
func TestRemote_AForgedAnnotationTakesNothing(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	a := remoteCR("team-a", "notes", "http://notes.team-a.svc:8080/mcp")
	r, _ := reconcilerAgainst(core, a)
	reconcileMCPServer(t, r, "notes", "team-a")

	evil := remoteCR("team-b", "notes", "http://attacker.team-b.svc:8080/mcp")
	evil.Annotations = map[string]string{"mcp-hangar.io/registered-by": "operator",
		"mcp-hangar.io/registered-endpoint": "http://stale:8080/mcp"}
	require.NoError(t, r.Create(context.Background(), evil))
	reconcileMCPServer(t, r, "notes", "team-b")
	require.NoError(t, r.deregisterOwned(context.Background(), getMCPServer(t, r, "notes", "team-b")))

	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": a.Spec.Endpoint}, servers)
}

// Core refusing a private endpoint it does not trust is a setting to change in
// core: the condition names it, the Warning fires once, and the CR is not
// re-asked every 10 seconds.
func TestRemote_UntrustedEndpointNamesTheCoreSetting(t *testing.T) {
	c := &registry{servers: map[string]string{}, refuseSSR: true}
	core := c.server(t)
	p := remoteCR("default", "internal", "http://10.244.1.7:8080/mcp")
	r, rec := reconcilerAgainst(core, p)

	result := reconcileMCPServer(t, r, "internal", "default")
	reconcileMCPServer(t, r, "internal", "default")

	degraded := degradedReason(t, r, "default", "internal")
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

// Claimed, and the process died before the POST: the next reconcile registers it.
func TestRemote_ClaimedButNotRegisteredIsRegistered(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://notes:8080/mcp")
	r, _ := reconcilerAgainst(core, p)
	_, err := r.Claims.take(context.Background(), p, p.Spec.Endpoint)
	require.NoError(t, err)

	reconcileMCPServer(t, r, "notes", "default")

	servers, _ := c.snapshot()
	assert.Equal(t, map[string]string{"notes": "http://notes:8080/mcp"}, servers)
}

// Core cannot change an endpoint: a changed spec.endpoint deregisters the
// server and registers it again, and the claim follows.
func TestRemote_ChangedEndpointIsRegisteredAgain(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://old:8080/mcp")
	r, _ := reconcilerAgainst(core, p)
	reconcileMCPServer(t, r, "notes", "default")

	cur := getMCPServer(t, r, "notes", "default")
	cur.Spec.Endpoint = "http://new:8080/mcp"
	require.NoError(t, r.Update(context.Background(), cur))
	reconcileMCPServer(t, r, "notes", "default")

	servers, calls := c.snapshot()
	assert.Equal(t, map[string]string{"notes": "http://new:8080/mcp"}, servers)
	joined := strings.Join(calls, "\n")
	assert.Less(t, strings.Index(joined, "DELETE"), strings.LastIndex(joined, "POST /api/mcp_servers/\n"),
		"removed before it is added again")
	assert.Equal(t, "http://new:8080/mcp", claimOf(t, r, "notes").Annotations[claimEndpointAnnotation])
}

// A key whose role cannot create servers -- provider-admin -- gets a condition
// that says so, not "check the API key", which is valid.
func TestRemote_RoleWithoutWriteSaysSo(t *testing.T) {
	c := &registry{servers: map[string]string{}, forbidPost: true}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://notes:8080/mcp")
	r, _ := reconcilerAgainst(core, p)

	result := reconcileMCPServer(t, r, "notes", "default")

	degraded := degradedReason(t, r, "default", "notes")
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonRegistrationForbidden, degraded.Reason)
	assert.Contains(t, degraded.Message, "mcp_servers:write")
	assert.Equal(t, readyRequeueAfter, result.RequeueAfter)
}

// Without a namespace to keep claims in, nothing is registered.
func TestRemote_NoClaimsNoRegistration(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	p := remoteCR("default", "notes", "http://notes:8080/mcp")
	r, _ := reconcilerAgainst(core, p)
	r.Claims = nil

	reconcileMCPServer(t, r, "notes", "default")

	servers, _ := c.snapshot()
	assert.Empty(t, servers)
}

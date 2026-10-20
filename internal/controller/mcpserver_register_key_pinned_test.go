package controller

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
)

// ADR-030 §4(a), (e): the operator registers a key-pinned remote MCPServer with
// its key list, only over https and only with a core that advertises the
// capability, and delivers a changed endpoint or key list in place.

const (
	keyA = "sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	keyB = "sha256:b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
)

// pinningCore is core over https, recording what it was sent.
type pinningCore struct {
	mu           sync.Mutex
	capabilities []string
	servers      map[string]map[string]any
	calls        []string
	updateStatus int
}

func (c *pinningCore) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls = append(c.calls, r.Method+" "+r.URL.Path)
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/mcp_servers/"), "/")[0]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/api/system/":
			_ = json.NewEncoder(w).Encode(map[string]any{"system": map[string]any{"capabilities": c.capabilities}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/mcp_servers/":
			c.servers[body["mcp_server_id"].(string)] = body
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut:
			if c.updateStatus != 0 {
				w.WriteHeader(c.updateStatus)
				return
			}
			for k, v := range body {
				c.servers[name][k] = v
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/health"):
			if c.servers[name] == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"consecutive_failures":0,"total_invocations":0,"total_failures":0,"success_rate":1,"can_retry":true}`))
		case strings.HasSuffix(r.URL.Path, "/tools"):
			_, _ = w.Write([]byte(`{"tools":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sent lists the calls with this method, or exactly this method and path.
func (c *pinningCore) sent(call string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, made := range c.calls {
		if made == call || strings.HasPrefix(made, call+" ") {
			out = append(out, made)
		}
	}
	return out
}

func overHTTPS(srv *httptest.Server, objs ...*mcpv1alpha2.MCPServer) (*MCPServerReconciler, *events.FakeRecorder) {
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	objects := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		objects = append(objects, o)
	}
	r := newMCPServerReconciler(objects...)
	r.HangarClient = hangar.NewClient(&hangar.Config{URL: srv.URL, MaxRetries: 1, TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}})
	r.Claims = &RegistrationClaims{Client: r.Client, Reader: r.Client, Namespace: operatorNamespace}
	rec := events.NewFakeRecorder(50)
	r.Recorder = rec
	return r, rec
}

func keyPinnedCR(name string, keys ...string) *mcpv1alpha2.MCPServer {
	s := remoteCR("default", name, "https://payments.svc.example:8443/mcp")
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{
		PeerIdentity:     &mcpv1alpha2.PeerIdentity{KeyRef: "lineage", SPKISHA256: keys},
		MaxConnectionAge: &metav1.Duration{Duration: 30 * time.Minute},
	}
	return s
}

func newCore(capabilities ...string) *pinningCore {
	return &pinningCore{capabilities: capabilities, servers: map[string]map[string]any{}}
}

func TestKeyPinned_IsRegisteredWithItsKeyList(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	r, _ := overHTTPS(core.serve(t), keyPinnedCR("payments", keyA))

	reconcileMCPServer(t, r, "payments", "default")

	registered := core.servers["payments"]
	require.NotNil(t, registered)
	assert.Equal(t, map[string]any{
		"peer_identity":      map[string]any{"key_ref": "lineage", "spki_sha256": []any{keyA}},
		"max_connection_age": float64(1800),
	}, registered["tls"])
	assert.NotEmpty(t, claimOf(t, r, "payments").Annotations[claimTLSAnnotation])
}

func TestKeyPinned_AChangedKeyListOrEndpointIsDeliveredInPlace(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	r, _ := overHTTPS(core.serve(t), keyPinnedCR("payments", keyA))
	reconcileMCPServer(t, r, "payments", "default")

	s := getMCPServer(t, r, "payments", "default")
	s.Spec.TLS.PeerIdentity.SPKISHA256 = []string{keyA, keyB}
	s.Spec.Endpoint = "https://payments-2.svc.example:8443/mcp"
	require.NoError(t, r.Update(t.Context(), s))
	reconcileMCPServer(t, r, "payments", "default")
	reconcileMCPServer(t, r, "payments", "default")

	assert.Len(t, core.sent(http.MethodPut), 1, "delivered once, then recorded on the claim")
	assert.Empty(t, core.sent(http.MethodDelete), "not removed and registered again")
	assert.Len(t, core.sent("POST /api/mcp_servers/"), 1)
	assert.Equal(t, "https://payments-2.svc.example:8443/mcp", core.servers["payments"]["endpoint"])
	assert.Equal(t, []any{keyA, keyB}, core.servers["payments"]["tls"].(map[string]any)["peer_identity"].(map[string]any)["spki_sha256"])
}

func TestKeyPinned_ARefusedDeliveryIsRetried(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	r, _ := overHTTPS(core.serve(t), keyPinnedCR("payments", keyA))
	reconcileMCPServer(t, r, "payments", "default")
	core.updateStatus = http.StatusConflict

	s := getMCPServer(t, r, "payments", "default")
	s.Spec.TLS.PeerIdentity.SPKISHA256 = []string{keyA, keyB}
	require.NoError(t, r.Update(t.Context(), s))
	result := reconcileMCPServer(t, r, "payments", "default")

	assert.Equal(t, errorRequeueAfter, result.RequeueAfter)
	assert.Equal(t, ReasonRegistrationFailed, degradedReason(t, r, "default", "payments").Reason)
	core.updateStatus = 0
	reconcileMCPServer(t, r, "payments", "default")
	assert.Len(t, core.sent(http.MethodPut), 2, "the next reconcile delivers again")
}

func TestKeyPinned_IsNotRegisteredWhereItsKeyWouldGoUnchecked(t *testing.T) {
	cases := map[string]struct {
		core    *pinningCore
		https   bool
		mutate  func(*mcpv1alpha2.MCPServer)
		message string
	}{
		"core without the capability": {newCore(), true, nil, "peer_identity capability"},
		"plaintext hangar.url":        {newCore(hangar.PeerIdentityCapability), false, nil, "only over https"},
		"a declared issuance": {newCore(hangar.PeerIdentityCapability), true, func(s *mcpv1alpha2.MCPServer) {
			s.Spec.TLS.PeerIdentity.Issuance = mcpv1alpha2.PeerKeyIssuanceKBSAttested
		}, "issuance"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cr := keyPinnedCR("payments", keyA)
			if c.mutate != nil {
				c.mutate(cr)
			}
			srv := c.core.serve(t)
			r, _ := overHTTPS(srv, cr)
			if !c.https {
				r.HangarClient = hangar.NewClient(&hangar.Config{URL: strings.Replace(srv.URL, "https://", "http://", 1), MaxRetries: 1})
			}

			reconcileMCPServer(t, r, "payments", "default")

			assert.Empty(t, c.core.sent("POST /api/mcp_servers/"), "nothing is registered")
			degraded := degradedReason(t, r, "default", "payments")
			require.NotNil(t, degraded)
			assert.Equal(t, ReasonPeerIdentityNotCarried, degraded.Reason)
			assert.Contains(t, degraded.Message, c.message)
		})
	}
}

package controller

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/metrics"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// #330: an operator against a core older than hangar.MinCoreVersion says so
// once, for the whole operator, and calls core for nothing that needs the
// newer one, instead of one Degraded MCPServer per refused registration.

func gateFor(t *testing.T, srv *httptest.Server) (*CoreGate, *events.FakeRecorder) {
	t.Helper()
	cfg := &hangar.Config{URL: srv.URL, MaxRetries: 1}
	if srv.TLS != nil {
		roots := x509.NewCertPool()
		roots.AddCert(srv.Certificate())
		cfg.TLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	rec := events.NewFakeRecorder(10)
	return &CoreGate{
		Client: hangar.NewClient(cfg), Recorder: rec, Interval: time.Minute, RetryInterval: time.Second,
		Self: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "operator-0", Namespace: operatorNamespace}},
	}, rec
}

func TestCoreGate_Verdicts(t *testing.T) {
	for _, tc := range []struct {
		version, reason string
	}{
		{"2.26.0", ReasonCoreIncompatible},
		{"0.0.0.dev", ReasonCoreIncompatible},
		{"", ReasonCoreVersionUnknown},
		{hangar.MinCoreVersion, ""},
		{"2.27.0.dev4", ""},
		{"2.36.1", ""},
	} {
		t.Run(tc.version, func(t *testing.T) {
			core := newCore()
			core.version = tc.version
			g, rec := gateFor(t, core.serve(t))
			assert.Equal(t, time.Minute, g.check(t.Context()))

			proceed, reason, msg := g.verdict()
			assert.Equal(t, tc.reason == "", proceed)
			assert.Equal(t, tc.reason, reason)
			evs := drain(rec)
			require.Len(t, evs, 1, "one Event regarding the operator")
			if tc.reason == "" {
				assert.True(t, strings.HasPrefix(evs[0], "Normal "+ReasonCoreCompatible), evs[0])
				return
			}
			assert.True(t, strings.HasPrefix(evs[0], "Warning "+tc.reason), evs[0])
			assert.Contains(t, msg, hangar.MinCoreVersion)
		})
	}
}

func TestCoreGate_AForbiddenVersionReadIsNotAssumedNew(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	g, _ := gateFor(t, srv)
	g.check(t.Context())

	proceed, reason, msg := g.verdict()
	assert.False(t, proceed)
	assert.Equal(t, ReasonCoreVersionUnknown, reason)
	assert.Contains(t, msg, "mcp_servers:read")
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.CoreCompatibility.WithLabelValues("unknown")))
}

// Unreachable at start is pending, not a verdict; the first answer is read as
// a reconnect, and an outage after it keeps the verdict it had.
func TestCoreGate_ReadsTheVersionAgainOnReconnect(t *testing.T) {
	var mu sync.Mutex
	up, version := false, "2.26.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			// Hijack and close: no HTTP answer, as from a core that is down.
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{"system":{"version":"` + version + `"}}`))
	}))
	t.Cleanup(srv.Close)
	set := func(u bool, v string) { mu.Lock(); up, version = u, v; mu.Unlock() }
	g, rec := gateFor(t, srv)

	assert.Equal(t, time.Second, g.check(t.Context()), "retried soon while core does not answer")
	proceed, reason, _ := g.verdict()
	assert.False(t, proceed)
	assert.Empty(t, reason, "pending")
	assert.Empty(t, drain(rec))

	set(true, "2.26.0")
	g.check(t.Context())
	_, reason, _ = g.verdict()
	assert.Equal(t, ReasonCoreIncompatible, reason)
	g.check(t.Context())
	assert.Len(t, drain(rec), 1, "an unchanged verdict is not reported again")

	set(false, "")
	g.check(t.Context())
	_, reason, _ = g.verdict()
	assert.Equal(t, ReasonCoreIncompatible, reason, "an outage keeps the verdict")

	set(true, "2.36.1")
	g.check(t.Context())
	proceed, _, _ = g.verdict()
	assert.True(t, proceed, "an upgraded core is read on reconnect")
	assert.Len(t, drain(rec), 1)
}

func TestOlderCore_NoServerIsRegisteredAndNoneIsDegraded(t *testing.T) {
	core := newCore()
	core.version = "2.26.0"
	srv := core.serve(t)
	remote := remoteCR("default", "remote", "http://upstream.default.svc:8080/mcp")
	box := plainContainer("box")
	r, rec := overHTTPS(srv, remote, box)
	podFor(r, t, box, "10.244.3.17")
	r.CoreGate, _ = gateFor(t, srv)
	r.CoreGate.check(t.Context())

	for _, name := range []string{"remote", "box"} {
		result := reconcileMCPServer(t, r, name, "default")
		assert.Equal(t, coreHeldRequeue, result.RequeueAfter, name)
		s := getMCPServer(t, r, name, "default")
		ready := meta.FindStatusCondition(s.Status.Conditions, ConditionReady)
		require.NotNil(t, ready, name)
		assert.Equal(t, metav1.ConditionFalse, ready.Status)
		assert.Equal(t, ReasonCoreIncompatible, ready.Reason)
		assert.False(t, meta.IsStatusConditionTrue(s.Status.Conditions, ConditionDegraded), name)
		assert.Zero(t, s.Status.ConsecutiveFailures, name)
	}
	assert.Equal(t, []string{"GET /api/system/"}, core.sent(http.MethodGet), "core was asked for its version only")
	assert.Empty(t, core.sent(http.MethodPost), "nothing registered or started")
	assert.Empty(t, drain(rec), "no Event per server")
}

func TestOlderCore_BeforeTheFirstAnswerNothingIsWritten(t *testing.T) {
	core := newCore()
	r, _ := overHTTPS(core.serve(t), remoteCR("default", "remote", "http://upstream.default.svc:8080/mcp"))
	r.CoreGate = &CoreGate{}

	result := reconcileMCPServer(t, r, "remote", "default")
	assert.Equal(t, corePendingRequeue, result.RequeueAfter)
	assert.Empty(t, getMCPServer(t, r, "remote", "default").Status.Conditions)
	assert.Empty(t, core.sent(http.MethodPost))
}

func TestCompatibleCore_RegistersAsBefore(t *testing.T) {
	core := newCore()
	core.version = "2.36.1"
	srv := core.serve(t)
	r, _ := overHTTPS(srv, remoteCR("default", "remote", "http://upstream.default.svc:8080/mcp"))
	r.CoreGate, _ = gateFor(t, srv)
	r.CoreGate.check(t.Context())

	reconcileMCPServer(t, r, "remote", "default")
	assert.NotNil(t, core.servers["remote"])
}

func TestOlderCore_NoL7PolicyIsDeliveredAndThePolicyIsNotDegraded(t *testing.T) {
	core := newCore()
	core.version = "2.26.0"
	srv := core.serve(t)
	p := testPolicy("pol", "default")
	r := withProbe(newEgressReconciler(remoteServer("srv", "default", nil), p), networkpolicy.EnforcementObserved)
	r.CoreGate, _ = gateFor(t, srv)
	r.HangarClient = r.CoreGate.Client
	r.CoreGate.check(t.Context())

	out := reconcilePolicy(t, r, p)

	assert.Empty(t, core.sent(http.MethodPost), "no L7 push")
	l7 := condStatus(out, EgressPolicyConditionL7Delivered)
	assert.Equal(t, metav1.ConditionFalse, l7.Status)
	assert.Equal(t, ReasonCoreIncompatible, l7.Reason)
	assert.False(t, meta.IsStatusConditionTrue(out.Status.Conditions, EgressPolicyConditionDegraded))
}

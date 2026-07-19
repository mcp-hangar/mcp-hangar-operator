package hangar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mcp-hangar/operator/pkg/metrics"
)

// A failed Hangar call increments the per-operation error counter; a successful
// one does not. (Latency is observed in the same deferred recorder.)
func TestClientRecordsErrorsPerOperation(t *testing.T) {
	// Error path: SetL7Policy against a 400 records an error for "set_l7_policy".
	before := testutil.ToFloat64(metrics.HangarClientErrors.WithLabelValues("set_l7_policy"))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer bad.Close()
	c := NewClient(&Config{URL: bad.URL, MaxRetries: 0})
	err := c.SetL7Policy(context.Background(), "srv", &L7PolicyPayload{DefaultAction: "Deny"})
	require.Error(t, err)
	assert.InDelta(t, before+1, testutil.ToFloat64(metrics.HangarClientErrors.WithLabelValues("set_l7_policy")), 0.001)

	// Success path: Ping to a 200 does not increment the "ping" error counter.
	beforePing := testutil.ToFloat64(metrics.HangarClientErrors.WithLabelValues("ping"))
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	require.NoError(t, NewClient(&Config{URL: ok.URL, MaxRetries: 0}).Ping(context.Background()))
	assert.InDelta(t, beforePing, testutil.ToFloat64(metrics.HangarClientErrors.WithLabelValues("ping")), 0.001)
}

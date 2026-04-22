package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestReconcileTotal(t *testing.T) {
	// Reset counter before test
	ReconcileTotal.Reset()

	// Increment counter
	ReconcileTotal.WithLabelValues("mcpserver", "success").Inc()
	ReconcileTotal.WithLabelValues("mcpserver", "success").Inc()
	ReconcileTotal.WithLabelValues("mcpserver", "error").Inc()

	// Verify metrics
	assert.Equal(t, float64(2), testutil.ToFloat64(ReconcileTotal.WithLabelValues("mcpserver", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(ReconcileTotal.WithLabelValues("mcpserver", "error")))
}

func TestReconcileDuration(t *testing.T) {
	// Observe some durations
	ReconcileDuration.WithLabelValues("mcpserver").Observe(0.1)
	ReconcileDuration.WithLabelValues("mcpserver").Observe(0.5)
	ReconcileDuration.WithLabelValues("mcpserver").Observe(1.2)

	// Verify that observations were recorded
	count := testutil.CollectAndCount(ReconcileDuration)
	assert.Greater(t, count, 0)
}

func TestMCPServerState(t *testing.T) {
	// Set provider states
	SetMCPServerState("default", "provider1", "Ready")
	SetMCPServerState("default", "provider2", "Degraded")

	// Verify metrics
	assert.Equal(t, float64(1), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider1", "Ready")))
	assert.Equal(t, float64(0), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider1", "Degraded")))
	assert.Equal(t, float64(1), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider2", "Degraded")))
}

func TestSetMCPServerState_ClearsOtherStates(t *testing.T) {
	// Set to Ready first
	SetMCPServerState("default", "provider1", "Ready")
	assert.Equal(t, float64(1), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider1", "Ready")))

	// Change to Degraded
	SetMCPServerState("default", "provider1", "Degraded")
	assert.Equal(t, float64(0), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider1", "Ready")))
	assert.Equal(t, float64(1), testutil.ToFloat64(MCPServerState.WithLabelValues("default", "provider1", "Degraded")))
}

func TestMCPServerToolsCount(t *testing.T) {
	// Set tool counts
	MCPServerToolsCount.WithLabelValues("default", "provider1").Set(5)
	MCPServerToolsCount.WithLabelValues("default", "provider2").Set(10)

	// Verify metrics
	assert.Equal(t, float64(5), testutil.ToFloat64(MCPServerToolsCount.WithLabelValues("default", "provider1")))
	assert.Equal(t, float64(10), testutil.ToFloat64(MCPServerToolsCount.WithLabelValues("default", "provider2")))
}

func TestMCPServerHealthCheckFailures(t *testing.T) {
	// Reset counter
	MCPServerHealthCheckFailures.Reset()

	// Increment failures
	MCPServerHealthCheckFailures.WithLabelValues("default", "provider1").Inc()
	MCPServerHealthCheckFailures.WithLabelValues("default", "provider1").Inc()
	MCPServerHealthCheckFailures.WithLabelValues("default", "provider1").Inc()

	// Verify count
	assert.Equal(t, float64(3), testutil.ToFloat64(MCPServerHealthCheckFailures.WithLabelValues("default", "provider1")))
}

func TestCRDCount(t *testing.T) {
	// Set CRD counts
	CRDCount.WithLabelValues("MCPServer").Set(25)
	CRDCount.WithLabelValues("MCPServerGroup").Set(5)
	CRDCount.WithLabelValues("MCPDiscoverySource").Set(3)

	// Verify metrics
	assert.Equal(t, float64(25), testutil.ToFloat64(CRDCount.WithLabelValues("MCPServer")))
	assert.Equal(t, float64(5), testutil.ToFloat64(CRDCount.WithLabelValues("MCPServerGroup")))
	assert.Equal(t, float64(3), testutil.ToFloat64(CRDCount.WithLabelValues("MCPDiscoverySource")))
}

func TestCapabilityViolationsTotal(t *testing.T) {
	CapabilityViolationsTotal.Reset()

	CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "egress_denied").Inc()
	CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "egress_denied").Inc()
	CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "capability_drift").Inc()

	assert.Equal(t, float64(2), testutil.ToFloat64(CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "egress_denied")))
	assert.Equal(t, float64(1), testutil.ToFloat64(CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "capability_drift")))
}

func TestRecordViolation(t *testing.T) {
	CapabilityViolationsTotal.Reset()

	RecordViolation("default", "test-provider", "undeclared_tool")
	RecordViolation("default", "test-provider", "undeclared_tool")
	RecordViolation("staging", "other-provider", "schema_mismatch")

	assert.Equal(t, float64(2), testutil.ToFloat64(CapabilityViolationsTotal.WithLabelValues("default", "test-provider", "undeclared_tool")))
	assert.Equal(t, float64(1), testutil.ToFloat64(CapabilityViolationsTotal.WithLabelValues("staging", "other-provider", "schema_mismatch")))
}

func TestMetricsRegistered(t *testing.T) {
	// Verify all metrics are registered
	metrics := []prometheus.Collector{
		ReconcileTotal,
		ReconcileDuration,
		MCPServerState,
		MCPServerToolsCount,
		MCPServerHealthCheckFailures,
		CRDCount,
		CapabilityViolationsTotal,
	}

	for _, metric := range metrics {
		assert.NotNil(t, metric, "Metric should be initialized")
	}
}

func TestReconcileTotal_Labels(t *testing.T) {
	// Test different controller types
	ReconcileTotal.Reset()

	ReconcileTotal.WithLabelValues("mcpserver", "success").Inc()
	ReconcileTotal.WithLabelValues("mcpservergroup", "success").Inc()
	ReconcileTotal.WithLabelValues("mcpdiscoverysource", "success").Inc()

	assert.Equal(t, float64(1), testutil.ToFloat64(ReconcileTotal.WithLabelValues("mcpserver", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(ReconcileTotal.WithLabelValues("mcpservergroup", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(ReconcileTotal.WithLabelValues("mcpdiscoverysource", "success")))
}

func TestMCPServerState_AllStates(t *testing.T) {
	states := []string{"Cold", "Initializing", "Ready", "Degraded", "Dead"}

	for _, state := range states {
		SetMCPServerState("default", "test-provider", state)

		// Only the current state should be 1
		for _, s := range states {
			expected := float64(0)
			if s == state {
				expected = 1
			}
			assert.Equal(t, expected, testutil.ToFloat64(MCPServerState.WithLabelValues("default", "test-provider", s)))
		}
	}
}

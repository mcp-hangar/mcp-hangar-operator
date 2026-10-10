package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// ClearProviderMetrics left the capability_violations_total series of a
// deleted server behind (#211).
func TestClearProviderMetrics_DropsViolationSeries(t *testing.T) {
	CapabilityViolationsTotal.WithLabelValues("ns-211", "srv-211", "egress").Inc()
	CapabilityViolationsTotal.WithLabelValues("ns-211", "srv-211", "tools").Inc()
	CapabilityViolationsTotal.WithLabelValues("ns-211", "other", "egress").Inc()
	before := testutil.CollectAndCount(CapabilityViolationsTotal)

	ClearProviderMetrics("ns-211", "srv-211")

	assert.Equal(t, before-2, testutil.CollectAndCount(CapabilityViolationsTotal))
}

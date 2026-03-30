package health_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mcp-hangar/operator/internal/health"
)

func TestLeaderChecker_NotElected(t *testing.T) {
	// Channel is open (not closed) -- not yet elected.
	ch := make(chan struct{})
	checker := health.NewLeaderChecker(ch)

	err := checker.Check(&http.Request{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not the leader")
}

func TestLeaderChecker_Elected(t *testing.T) {
	// Closing the channel signals that leader election succeeded.
	ch := make(chan struct{})
	close(ch)
	checker := health.NewLeaderChecker(ch)

	err := checker.Check(&http.Request{})
	assert.NoError(t, err)
}

func TestLeaderChecker_ElectedAfterInitialFailure(t *testing.T) {
	ch := make(chan struct{})
	checker := health.NewLeaderChecker(ch)

	// Before election: should fail.
	err := checker.Check(&http.Request{})
	assert.Error(t, err)

	// Simulate election win.
	close(ch)

	// After election: should succeed.
	err = checker.Check(&http.Request{})
	assert.NoError(t, err)
}

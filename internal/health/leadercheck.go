// Package health provides health check implementations for the operator.
package health

import (
	"fmt"
	"net/http"
)

// LeaderChecker implements healthz.Checker and reports healthy only when this
// replica has been elected leader. Non-leader replicas return an error so that
// readiness probes direct traffic (e.g. webhook calls) exclusively to the
// active leader.
//
// The elected channel must be the one returned by manager.Elected(). It is
// closed when the manager wins the leader election; it remains open otherwise.
type LeaderChecker struct {
	elected <-chan struct{}
}

// NewLeaderChecker returns a new LeaderChecker that uses the given channel to
// determine election status. Pass mgr.Elected() as the channel.
func NewLeaderChecker(elected <-chan struct{}) *LeaderChecker {
	return &LeaderChecker{elected: elected}
}

// Check satisfies the healthz.Checker interface. It returns nil when this
// replica is the elected leader and an error otherwise.
func (c *LeaderChecker) Check(_ *http.Request) error {
	select {
	case <-c.elected:
		return nil
	default:
		return fmt.Errorf("not the leader")
	}
}

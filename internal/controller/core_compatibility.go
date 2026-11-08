package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/metrics"
)

const (
	// ReasonCoreIncompatible: core reported a version older than
	// hangar.MinCoreVersion, so the operator does not call it (#330).
	ReasonCoreIncompatible = "CoreIncompatible"
	// ReasonCoreVersionUnknown: core answered, but not with a version the
	// operator can compare (an auth refusal, or no release version). It is
	// held the same way: an unverified core is not assumed to be new enough.
	ReasonCoreVersionUnknown = "CoreVersionUnknown"
	// ReasonCoreCompatible is the Event when core meets the minimum.
	ReasonCoreCompatible = "CoreCompatible"

	// coreHeldRequeue paces an object waiting for core: short, so an
	// upgraded core is picked up within about a minute.
	coreHeldRequeue = time.Minute
	// corePendingRequeue paces an object reconciled before core's version
	// was first read.
	corePendingRequeue = 10 * time.Second
)

// CoreGate reads core's version on start, again on every reconnect and once
// per Interval, and tells the reconcilers whether they may call core (#330).
// One verdict for the whole operator, not one failure per object: an older
// core used to show up as a Degraded MCPServer per registration attempt.
//
// It is reported where an operator-wide state can go without a CRD of its own:
// an Event regarding the operator's Pod, a log line and the
// mcp_operator_core_compatibility gauge, each on a change of verdict.
type CoreGate struct {
	Client   *hangar.Client
	Recorder events.EventRecorder
	// Self is what the Events regard: this operator's Pod. Nil: no Events.
	Self runtime.Object
	// Interval between checks once core has answered; RetryInterval while it
	// has not.
	Interval, RetryInterval time.Duration

	mu        sync.RWMutex
	reason    string // "" compatible or pending; else the held reason
	message   string
	checked   bool
	reachable bool
}

// OperatorPod is the operator's own Pod, for Events that regard the operator
// rather than one object: the pod's hostname is its name. Nil out of cluster.
func OperatorPod() runtime.Object {
	name, err := os.Hostname()
	ns := RegistrationNamespace()
	if err != nil || ns == "" {
		return nil
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

// NeedLeaderElection is false: a standby that becomes leader then already
// holds a verdict.
func (g *CoreGate) NeedLeaderElection() bool { return false }

// Start checks until the manager stops.
func (g *CoreGate) Start(ctx context.Context) error {
	metrics.SetCoreCompatibility("pending")
	for {
		wait := g.check(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (g *CoreGate) check(ctx context.Context) time.Duration {
	logger := log.FromContext(ctx).WithName("core-gate")
	info, err := g.Client.SystemInfo(ctx)
	var se *hangar.StatusError
	if err != nil && !errors.As(err, &se) {
		// No answer: the last verdict stands, and the next answer is a
		// reconnect that reads the version again.
		g.mu.Lock()
		was := g.reachable || !g.checked
		g.reachable, g.checked = false, true
		g.mu.Unlock()
		if was {
			logger.Error(err, "core is unreachable; its version is read again when it answers")
		}
		return g.RetryInterval
	}

	reason, msg := "", ""
	switch {
	case err != nil:
		reason, msg = ReasonCoreVersionUnknown, fmt.Sprintf("core's version could not be read: %v; "+
			"the operator's API key needs mcp_servers:read at global scope, and core %s or newer",
			err, hangar.MinCoreVersion)
	default:
		ok, verr := hangar.CoreVersionSupported(info.Version)
		switch {
		case verr != nil:
			reason, msg = ReasonCoreVersionUnknown, fmt.Sprintf("%v; the operator needs core %s or newer "+
				"and calls it for nothing until it can tell", verr, hangar.MinCoreVersion)
		case !ok:
			reason, msg = ReasonCoreIncompatible, fmt.Sprintf("core %s is older than %s, the oldest this "+
				"operator supports; it registers no MCPServer and delivers no L7 policy until core is upgraded",
				info.Version, hangar.MinCoreVersion)
		default:
			msg = fmt.Sprintf("core %s meets the minimum %s", info.Version, hangar.MinCoreVersion)
		}
	}

	g.mu.Lock()
	changed := !g.reachable || reason != g.reason || msg != g.message
	g.reason, g.message, g.checked, g.reachable = reason, msg, true, true
	g.mu.Unlock()
	if !changed {
		return g.Interval
	}

	eventType, eventReason, state := corev1.EventTypeNormal, ReasonCoreCompatible, "compatible"
	if reason != "" {
		eventType, eventReason, state = corev1.EventTypeWarning, reason, "unknown"
		if reason == ReasonCoreIncompatible {
			state = "incompatible"
		}
		logger.Error(nil, msg, "minimum", hangar.MinCoreVersion)
	} else {
		logger.Info(msg)
	}
	metrics.SetCoreCompatibility(state)
	if g.Self != nil && g.Recorder != nil {
		g.Recorder.Eventf(g.Self, nil, eventType, eventReason, "CheckCoreVersion", "%s", msg)
	}
	return g.Interval
}

// verdict says whether a reconciler may call core. A nil gate always may.
// held with an empty reason: core has not answered yet since start.
func (g *CoreGate) verdict() (proceed bool, reason, message string) {
	if g == nil {
		return true, "", ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.checked || (g.reason == "" && g.message == "") {
		return false, "", ""
	}
	return g.reason == "", g.reason, g.message
}

// requeue is how soon an object held by the gate is looked at again; zero
// when it is not held.
func (g *CoreGate) requeue() time.Duration {
	proceed, reason, _ := g.verdict()
	switch {
	case proceed:
		return 0
	case reason == "":
		return corePendingRequeue
	}
	return coreHeldRequeue
}

// holdForCore stops an MCPServer reconcile before it calls core when the gate
// says not to. A held server reports Ready=False with the gate's reason and is
// not Degraded: nothing about the server failed. Before core's first answer it
// writes nothing, so a restart does not flip every server's status.
func (r *MCPServerReconciler) holdForCore(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (bool, ctrl.Result, error) {
	proceed, reason, msg := r.CoreGate.verdict()
	if proceed {
		return false, ctrl.Result{}, nil
	}
	result := ctrl.Result{RequeueAfter: r.CoreGate.requeue()}
	if reason == "" {
		return true, result, nil
	}
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, reason, msg)
	return true, result, writeStatus(ctx, r.Client, mcpServer)
}

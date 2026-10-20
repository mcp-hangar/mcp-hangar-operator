// Package controller implements Kubernetes controllers for MCP resources
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/metrics"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
	"github.com/mcp-hangar/operator/pkg/provider"
)

const (
	// Finalizer name
	finalizerName = "mcp-hangar.io/finalizer"

	// Condition types
	ConditionReady                = "Ready"
	ConditionProgressing          = "Progressing"
	ConditionDegraded             = "Degraded"
	ConditionAvailable            = "Available"
	ConditionNetworkPolicyApplied = "NetworkPolicyApplied"
	ConditionViolationDetected    = "ViolationDetected"

	// Requeue intervals
	defaultRequeueAfter = 30 * time.Second
	errorRequeueAfter   = 10 * time.Second
	readyRequeueAfter   = 5 * time.Minute
	coldRequeueAfter    = 10 * time.Minute

	// maxConsecutiveFailures caps Status.ConsecutiveFailures, which mirrors
	// the pod's restart count (#221) and the failed core probes, so the
	// counter cannot grow without bound while a provider stays unhealthy.
	maxConsecutiveFailures = int32(5)

	// ActionReconcile is the events.k8s.io `action` field on every Event this
	// operator emits. The new events API splits what the legacy API called a
	// reason into reason + action; every event here is emitted from a reconcile
	// loop, and the reason (preserved verbatim from the legacy API, because it
	// is what `kubectl get events` shows and what people alert on) already
	// carries the specifics. A second, finer taxonomy nothing consumes would
	// just drift.
	ActionReconcile = "Reconcile"

	// Event reasons
	ReasonCreated                   = "Created"
	ReasonUpdated                   = "Updated"
	ReasonDeleted                   = "Deleted"
	ReasonFailed                    = "Failed"
	ReasonReady                     = "Ready"
	ReasonDegraded                  = "Degraded"
	ReasonStarting                  = "Starting"
	ReasonStopping                  = "Stopping"
	ReasonHealthy                   = "Healthy"
	ReasonUnhealthy                 = "Unhealthy"
	ReasonViolationDetected         = "ViolationDetected"
	ReasonViolationCleared          = "ViolationCleared"
	ReasonUnrestrictedEgressAllowed = "UnrestrictedEgressAllowed"
	ReasonCoreAuthRejected          = "CoreAuthRejected"
	ReasonDeregistrationSkipped     = "DeregistrationSkipped"
	// ReasonRegistered: the operator added the remote server to core (#164).
	ReasonRegistered = "Registered"
	// ReasonEndpointNotTrusted: core refused the endpoint as a private address
	// outside its ssrf.trusted_networks.
	ReasonEndpointNotTrusted = "EndpointNotTrusted"
	// ReasonPeerIdentityNotCarried: the CR declares spec.tls.peerIdentity, and
	// its key list cannot reach core: a container-mode server, a declared
	// issuance, a plaintext hangar.url, or a core without the capability.
	ReasonPeerIdentityNotCarried = "PeerIdentityNotCarried"
	// ReasonRegistrationForbidden: core accepted the operator's API key and
	// refused it the registration (403): its role cannot create servers.
	ReasonRegistrationForbidden = "RegistrationForbidden"
	// ReasonNameTaken: core's server name -- the CR's name, which core keys
	// cluster-wide -- is registered for another MCPServer, or held by core
	// itself.
	ReasonNameTaken = "NameTaken"
	// ReasonStartFailed: core holds the registered server and could not start
	// it, so nothing has shown the endpoint answers.
	ReasonStartFailed = "StartFailed"
	// ReasonRegistrationFailed: core did not take the registration, for a
	// reason other than the SSRF refusal.
	ReasonRegistrationFailed = "RegistrationFailed"
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     events.EventRecorder
	HangarClient *hangar.Client
	// EnforcementProbe reports whether the API server this operator writes to
	// has anything that enforces a NetworkPolicy (#199). NetworkPolicyApplied
	// is True only when it does; nil reports Unknown, which surfaces as
	// Unknown rather than as a claim either way.
	EnforcementProbe *networkpolicy.EnforcementProbe
	ProviderIngress  networkpolicy.ProviderIngressConfig
	// Claims records which MCPServer registered each server name in core
	// (#164). Nil: the operator registers nothing and only mirrors.
	Claims *RegistrationClaims
	// MaxConcurrentReconciles is how many MCPServers reconcile at once; zero
	// keeps controller-runtime's default of one. With one worker, a reconcile
	// stuck on a slow core held up every other server's, pod create and
	// delete included (#201).
	MaxConcurrentReconciles int
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// Events now go to events.k8s.io/v1 (the recorder migration in #58). The
// core/v1 grant stays because controller-runtime's leader election still emits
// through the legacy events API; it can go when that does.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

// Reconcile performs the reconciliation loop for MCPServer
func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Reconciling MCPServer", "namespacedName", req.NamespacedName)

	// Fetch the MCPServer instance
	mcpServer := &mcpv1alpha2.MCPServer{}
	if err := r.Get(ctx, req.NamespacedName, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPServer resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPServer")
		return ctrl.Result{}, err
	}
	ctx = withStatusBase(ctx, mcpServer)

	// Handle deletion
	if !mcpServer.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, mcpServer)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(mcpServer, finalizerName) {
		controllerutil.AddFinalizer(mcpServer, finalizerName)
		if err := r.Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		// Carry on in the same pass: Requeue is deprecated in controller-runtime
		// 0.25, and the update refreshed the object's resourceVersion (#210).
	}

	// Main reconciliation logic
	return r.reconcileNormal(ctx, mcpServer)
}

// reconcileNormal handles normal (non-deletion) reconciliation
func (r *MCPServerReconciler) reconcileNormal(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Update observed generation
	if mcpServer.Status.ObservedGeneration != mcpServer.Generation {
		mcpServer.Status.ObservedGeneration = mcpServer.Generation
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "Reconciling", "Processing spec changes")
	}

	if err := r.reconcileProviderIngress(ctx, mcpServer); err != nil {
		setServerCondition(mcpServer, ConditionProviderIngressApplied, metav1.ConditionFalse, "ApplyFailed", err.Error())
		if statusErr := writeStatus(ctx, r.Client, mcpServer); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	// Declared, not yet carried for a container: running its pod before the
	// operator registers it with its key list would have core discover it as a
	// plain server whose key nothing checks. It is held, and says so.
	if keyPinned(mcpServer) && mcpServer.Spec.Mode == mcpv1alpha2.MCPServerModeContainer {
		return ctrl.Result{}, r.holdPeerIdentity(ctx, mcpServer)
	}

	// Route based on mode
	switch mcpServer.Spec.Mode {
	case mcpv1alpha2.MCPServerModeContainer:
		return r.reconcileContainerProvider(ctx, mcpServer)
	case mcpv1alpha2.MCPServerModeRemote:
		return r.reconcileRemoteProvider(ctx, mcpServer)
	default:
		// defense-in-depth: unreachable while the CRD schema enforces
		// spec.mode via +kubebuilder:validation:Enum=container;remote, so a
		// persisted object can never carry an unknown mode. Kept as a guard
		// against future enum additions or direct-cache manipulation.
		logger.Error(nil, "Unknown provider mode", "mode", mcpServer.Spec.Mode)
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "InvalidMode", "Unknown provider mode")
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
}

// reconcileContainerProvider handles container-mode providers
func (r *MCPServerReconciler) reconcileContainerProvider(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate image is specified
	if mcpServer.Spec.Image == "" {
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "InvalidSpec", "Container mode requires image")
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
			"Container mode requires image")
		return ctrl.Result{}, nil
	}

	// Reconcile NetworkPolicy (independent of Pod lifecycle)
	if err := r.reconcileNetworkPolicy(ctx, mcpServer); err != nil {
		logger.Error(err, "Failed to reconcile NetworkPolicy")
		// Non-blocking: log error but continue with Pod reconciliation
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, "NetworkPolicyFailed", ActionReconcile,
			"Failed to reconcile NetworkPolicy: %v", err)
	}

	// Reconcile violation detection (after NetworkPolicy, before Pod lifecycle)
	if err := r.reconcileViolationDetection(ctx, mcpServer); err != nil {
		logger.Error(err, "Failed to reconcile violation detection")
		// Non-blocking: log error but continue with Pod reconciliation
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, "ViolationDetectionFailed", ActionReconcile,
			"Failed to detect violations: %v", err)
	}

	// Audit wildcard egress override usage (emits Warning event for audit trail)
	r.reconcileEgressAudit(ctx, mcpServer)

	return r.reconcileDeployment(ctx, mcpServer)
}

// syncPodStatus synchronizes MCPServer status with the Deployment's pod.
//
// Under the Deployment the pod restarts in place (RestartPolicy Always), so a
// crashing server is not a Failed pod but a Running or Pending one whose
// container waits in CrashLoopBackOff with a rising restart count. That is
// read first; the phase only says what is left (#221).
func (r *MCPServerReconciler) syncPodStatus(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, pod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	requeueAfter := defaultRequeueAfter

	if reason, msg, restarts, stuck := containerStuck(pod); stuck {
		requeueAfter = r.reportPodStuck(mcpServer, reason, msg, restarts)
	} else {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			requeueAfter = r.handlePodRunning(ctx, mcpServer, pod)
		case corev1.PodPending:
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
			mcpServer.Status.ReadyReplicas = 0
			setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "PodPending", "Pod is pending")
			metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Initializing")
		case corev1.PodFailed:
			// Evicted, typically; the ReplicaSet starts a replacement.
			requeueAfter = r.reportPodStuck(mcpServer, "PodFailed", pod.Status.Reason+": "+pod.Status.Message, 0)
		default:
			logger.Info("Unhandled pod phase", "phase", pod.Status.Phase)
		}
	}

	mcpServer.Status.PodName = pod.Name
	mcpServer.Status.Replicas = 1

	// Propagate capabilities from spec to status (Phase 38).
	if mcpServer.Spec.Capabilities != nil {
		mcpServer.Status.Capabilities = mcpServer.Spec.Capabilities.DeepCopy()
	} else {
		mcpServer.Status.Capabilities = nil
	}

	if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// handlePodRunning handles a running Pod
func (r *MCPServerReconciler) handlePodRunning(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, pod *corev1.Pod) time.Duration {
	logger := log.FromContext(ctx)

	// Check if all containers are ready
	allReady := true
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			allReady = false
			break
		}
	}

	if !allReady {
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
		mcpServer.Status.ReadyReplicas = 0
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "ContainersStarting", "Waiting for containers to be ready")
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Initializing")
		return defaultRequeueAfter
	}

	// All containers ready - probe MCP-Hangar for tools
	if r.HangarClient != nil {
		tools, err := r.HangarClient.GetMCPServerTools(ctx, mcpServer.Name, mcpServer.Namespace)
		if err != nil {
			logger.Error(err, "Failed to get provider tools from Hangar")
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateDegraded
			// Cap the counter (mirrors handlePodFailed) so a long-unreachable
			// Hangar cannot grow ConsecutiveFailures without bound.
			if mcpServer.Status.ConsecutiveFailures < maxConsecutiveFailures {
				mcpServer.Status.ConsecutiveFailures++
			}
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "ToolsFetchFailed", err.Error())
			metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Degraded")
			metrics.MCPServerHealthCheckFailures.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
			return defaultRequeueAfter
		}

		mcpServer.Status.Tools = tools
		mcpServer.Status.ToolsCount = int32(len(tools))
		metrics.MCPServerToolsCount.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Set(float64(len(tools)))
	}

	// Provider is ready
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateReady
	mcpServer.Status.ReadyReplicas = 1
	mcpServer.Status.AvailableReplicas = 1
	mcpServer.Status.ConsecutiveFailures = 0
	now := metav1.Now()
	mcpServer.Status.LastHealthCheck = &now

	becameReady := conditionChanges(mcpServer.Status.Conditions, ConditionReady, metav1.ConditionTrue, "ProviderReady")
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionTrue, "ProviderReady", "Provider is ready")
	setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionFalse, "Reconciled", "")
	setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionFalse, "Healthy", "Provider is healthy")
	setServerCondition(mcpServer, ConditionAvailable, metav1.ConditionTrue, "Available", "Provider is available")

	if becameReady { // not on every 5-minute poll (#210)
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonReady, ActionReconcile,
			"Provider is ready")
	}
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Ready")

	return readyRequeueAfter
}

// reportPodStuck records a server whose pod cannot run: a container in
// CrashLoopBackOff or an image that cannot be pulled, or a failed pod. Kubelet
// and the ReplicaSet do the restarting; the operator only reports, once per
// change, with the restart count as ConsecutiveFailures (capped, as before).
func (r *MCPServerReconciler) reportPodStuck(mcpServer *mcpv1alpha2.MCPServer, reason, msg string, restarts int32) time.Duration {
	if restarts > maxConsecutiveFailures {
		restarts = maxConsecutiveFailures
	}
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
	mcpServer.Status.ConsecutiveFailures = restarts
	mcpServer.Status.ReadyReplicas = 0
	mcpServer.Status.AvailableReplicas = 0
	if conditionChanges(mcpServer.Status.Conditions, ConditionReady, metav1.ConditionFalse, reason) {
		now := metav1.Now()
		mcpServer.Status.LastStoppedAt = &now
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
			"Pod cannot run: %s: %s", reason, msg)
	}
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, reason, msg)
	setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, reason, msg)
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Dead")
	return defaultRequeueAfter
}

// holdPeerIdentity reports a key-pinned CR this operator does not yet run or
// register, and does neither.
func (r *MCPServerReconciler) holdPeerIdentity(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	msg := "spec.tls.peerIdentity is declared on a container-mode server, and this operator does not yet run one " +
		"with its key list; its pod is not created, rather than served without its key checked"
	if conditionChanges(mcpServer.Status.Conditions, ConditionReady, metav1.ConditionFalse, ReasonPeerIdentityNotCarried) {
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonPeerIdentityNotCarried, ActionReconcile, "%s", msg)
	}
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateCold
	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, ReasonPeerIdentityNotCarried, msg)
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpServer.Status.State))
	return writeStatus(ctx, r.Client, mcpServer)
}

// reconcileRemoteProvider handles remote-mode providers
// Note: NetworkPolicy is not reconciled for remote providers (no pods to target)
func (r *MCPServerReconciler) reconcileRemoteProvider(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate endpoint
	endpoint := mcpServer.Spec.Endpoint
	if endpoint == "" {
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "NoEndpoint", "Remote provider requires endpoint")
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
		if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// requeueAfter defaults to the slow, steady-state cadence and is shortened
	// below whenever the endpoint is unhealthy so recovery is detected quickly.
	requeueAfter := readyRequeueAfter

	// Health check via MCP-Hangar core (if client available)
	if r.HangarClient != nil {
		// Two distinct failures, kept distinct: we could not ask core, or core
		// says the upstream is failing. The first is our problem, the second is
		// the upstream's, and conflating them made both unreadable.
		if done, result, err := r.reconcileRegistration(ctx, mcpServer); done || err != nil {
			return result, err
		}
		health, err := r.HangarClient.GetMCPServerHealth(ctx, mcpServer.Name, mcpServer.Namespace)
		if hangar.IsAuthRejected(err) {
			// Core refused the operator's credentials. That is a deployment
			// fault, not an outage: re-asking every 10 s changes nothing, and
			// reporting it as a failed health check hid the cause (#212). One
			// Warning on the transition, then the steady cadence.
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateDegraded
			if c := meta.FindStatusCondition(mcpServer.Status.Conditions, ConditionDegraded); c == nil || c.Reason != ReasonCoreAuthRejected {
				r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonCoreAuthRejected, ActionReconcile,
					"Core rejected the operator's credentials; check --hangar-api-key: %v", err)
			}
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, ReasonCoreAuthRejected,
				"core rejected the operator's credentials (check the API key): "+err.Error())
			requeueAfter = readyRequeueAfter
		} else if err != nil {
			logger.Error(err, "Could not read health from Hangar core")
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateDegraded
			// ConsecutiveFailures is deliberately NOT touched here. It mirrors
			// what core observed against the upstream -- the branch below says
			// so -- and this branch is the one where core could not be asked at
			// all, so there is nothing new to mirror.
			//
			// Incrementing it here also made the status differ on every single
			// reconcile, and this controller watches its own resource: each
			// status write produced an update event, which produced another
			// reconcile, immediately. Measured on a live cluster against a
			// server core did not know: 168 failures per second and 1.8 million
			// on the counter. `errorRequeueAfter` is 10s and was never reached,
			// because the watch event always arrived first.
			// One Warning when the server enters this state, not one per 10 s
			// re-probe for as long as core stays unreachable (#210).
			if conditionChanges(mcpServer.Status.Conditions, ConditionDegraded, metav1.ConditionTrue, "HealthCheckFailed") {
				r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonUnhealthy, ActionReconcile,
					"Health check failed: %v", err)
			}
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "HealthCheckFailed", err.Error())
			metrics.MCPServerHealthCheckFailures.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
			// Re-probe soon so recovery is detected fast, not after the full readyRequeueAfter window.
			requeueAfter = errorRequeueAfter
		} else if health.ConsecutiveFailures == 0 {
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateReady
			mcpServer.Status.ConsecutiveFailures = 0
			now := metav1.Now()
			mcpServer.Status.LastHealthCheck = &now
			if conditionChanges(mcpServer.Status.Conditions, ConditionReady, metav1.ConditionTrue, "EndpointHealthy") {
				r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonHealthy, ActionReconcile,
					"Remote endpoint is healthy")
			}
			setServerCondition(mcpServer, ConditionReady, metav1.ConditionTrue, "EndpointHealthy", "Remote endpoint is healthy")

			// Tools come from the read model, which is the right source for a
			// catalogue and the wrong one for liveness -- hence the separate
			// call above. A failure here degrades the tool list, not the state.
			if tools, toolErr := r.HangarClient.GetMCPServerTools(ctx, mcpServer.Name, mcpServer.Namespace); toolErr != nil {
				logger.Error(toolErr, "Healthy, but could not read the tool list")
			} else {
				mcpServer.Status.Tools = tools
				mcpServer.Status.ToolsCount = int32(len(tools))
				metrics.MCPServerToolsCount.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Set(float64(len(tools)))
			}
		} else {
			// Mirror core's counter rather than keeping our own. Ours counted
			// probe attempts; this counts what core actually observed against
			// the upstream, which is the number an operator wants to see.
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateDegraded
			mcpServer.Status.ConsecutiveFailures = int32(health.ConsecutiveFailures)
			if conditionChanges(mcpServer.Status.Conditions, ConditionDegraded, metav1.ConditionTrue, "EndpointUnhealthy") {
				r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonUnhealthy, ActionReconcile,
					"Remote endpoint unhealthy")
			}
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "EndpointUnhealthy",
				fmt.Sprintf("Core reports %d consecutive failures (success rate %.2f)", health.ConsecutiveFailures, health.SuccessRate))
			// Re-probe soon so recovery is detected fast, not after the full readyRequeueAfter window.
			requeueAfter = errorRequeueAfter
		}
	} else {
		// No Hangar client - just mark as ready (assume healthy)
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateReady
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionTrue, "Assumed", "No Hangar client, assuming healthy")
	}

	mcpServer.Status.Endpoint = endpoint
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpServer.Status.State))

	// Propagate capabilities from spec to status (Phase 38).
	// Phase 39 may enrich status.capabilities with resolved IPs and computed fields.
	if mcpServer.Spec.Capabilities != nil {
		mcpServer.Status.Capabilities = mcpServer.Spec.Capabilities.DeepCopy()
	} else {
		mcpServer.Status.Capabilities = nil
	}

	if err := writeStatus(ctx, r.Client, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileNetworkPolicy ensures the NetworkPolicy for a provider matches its capabilities.
// Creates, updates, or deletes the NetworkPolicy as needed.
func (r *MCPServerReconciler) reconcileNetworkPolicy(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	logger := log.FromContext(ctx)

	// Pin-coupling (#51/#52): in a namespace opted into egress enforcement, a
	// registered container-mode server's per-server egress allow-policy is opened
	// only if its image is digest-pinned (or opted out via allow-mutable-image).
	// An unpinned server stays under the namespace default-deny (DNS only) --
	// registered, but not trusted to reach upstreams until the image is pinned.
	// Only applies in governed namespaces; elsewhere there is no default-deny
	// backstop, so withholding the allow-policy would not restrict anything.
	if mcpServer.IsContainerMode() &&
		!webhook.IsImageDigestPinned(mcpServer.Spec.Image, mcpServer.Annotations) {
		governed, err := r.namespaceEnforcesEgress(ctx, mcpServer.Namespace)
		if err != nil {
			return err
		}
		if governed {
			if err := r.deleteNetworkPolicyIfExists(ctx, mcpServer); err != nil {
				return err
			}
			logger.Info("Withholding egress: image not digest-pinned in enforce-egress namespace",
				"provider", mcpServer.Name, "image", mcpServer.Spec.Image)
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, "EgressWithheld", ActionReconcile,
				"egress not opened: spec.image is not digest-pinned in an enforce-egress namespace; "+"pin the image (image@sha256:...) or set annotation hangar.io/allow-mutable-image=\"true\" (#51/#52)")
			setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionFalse,
				"EgressWithheldUnpinnedImage",
				"Egress withheld: image not digest-pinned in an enforce-egress namespace")
			return nil
		}
	}

	desired := networkpolicy.BuildNetworkPolicy(mcpServer)

	if desired == nil {
		// No capabilities declared -- delete existing policy if any, clear condition
		if err := r.deleteNetworkPolicyIfExists(ctx, mcpServer); err != nil {
			return err
		}
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionFalse,
			"NoPolicyNeeded", "No network capabilities declared")
		return nil
	}

	// Set OwnerReference so K8s GC deletes NetworkPolicy when MCPServer is deleted
	if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on NetworkPolicy: %w", err)
	}

	// Check if NetworkPolicy already exists
	existing := &networkingv1.NetworkPolicy{}
	npKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.Get(ctx, npKey, existing)

	if errors.IsNotFound(err) {
		// Create
		logger.Info("Creating NetworkPolicy for provider",
			"networkPolicy", desired.Name, "provider", mcpServer.Name)
		if err := applyOwned(ctx, r.Client, desired); err != nil {
			return fmt.Errorf("failed to create NetworkPolicy: %w", err)
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, "NetworkPolicyCreated", ActionReconcile,
			"Created NetworkPolicy %s", desired.Name)
		r.recordPolicyEnforcement(ctx, mcpServer, fmt.Sprintf("NetworkPolicy %s created", desired.Name))
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy: %w", err)
	}

	// A same-named policy someone else controls is not ours to rewrite: the
	// update below used to overwrite its spec and adopt it (#210). Say so
	// instead, the way the namespace default-deny does for its name (#204).
	if !perServerPolicyOwned(mcpServer, existing) {
		if c := meta.FindStatusCondition(mcpServer.Status.Conditions, ConditionNetworkPolicyApplied); c == nil || c.Reason != ReasonPolicyNameTaken {
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonPolicyNameTaken, ActionReconcile,
				"NetworkPolicy %s exists and is not managed by this operator; not overwriting it", existing.Name)
		}
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionFalse, ReasonPolicyNameTaken,
			fmt.Sprintf("NetworkPolicy %s exists and is not managed by this operator; the server's egress policy is not applied", existing.Name))
		return nil
	}

	// Apply if what the operator sets has drifted, or adopt a policy this
	// operator wrote before it set owner references. An apply leaves alone what
	// it does not set, so a label someone else added survives (#220).
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) || !metav1.IsControlledBy(existing, mcpServer) ||
		!labelsContain(existing.Labels, desired.Labels) || !labelsContain(existing.Annotations, desired.Annotations) {
		logger.Info("Updating NetworkPolicy for provider",
			"networkPolicy", desired.Name, "provider", mcpServer.Name)
		if err := applyOwned(ctx, r.Client, desired); err != nil {
			return fmt.Errorf("failed to update NetworkPolicy: %w", err)
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, "NetworkPolicyUpdated", ActionReconcile,
			"Updated NetworkPolicy %s", desired.Name)
	}

	r.recordPolicyEnforcement(ctx, mcpServer, fmt.Sprintf("NetworkPolicy %s applied", desired.Name))
	return nil
}

// conditionChanges reports whether setting condition t to status/reason would
// change it -- the moment worth an Event. Emitting on every poll instead put a
// Warning in the event stream every 10 s per unhealthy server (#210).
func conditionChanges(conds []metav1.Condition, t string, status metav1.ConditionStatus, reason string) bool {
	c := meta.FindStatusCondition(conds, t)
	return c == nil || c.Status != status || c.Reason != reason
}

// Reasons on NetworkPolicyApplied for a policy that was written but whose
// enforcement the probe could not confirm (#199).
const (
	// ReasonPolicyWrittenUnenforced: the policy exists, and nothing in this
	// API server enforces NetworkPolicy, so it restricts nothing.
	ReasonPolicyWrittenUnenforced = "PolicyWrittenUnenforced"
	// ReasonPolicyWrittenUnverified: the policy exists, and the probe could
	// not tell whether anything enforces it.
	ReasonPolicyWrittenUnverified = "PolicyWrittenUnverified"
	// ReasonPolicyNameTaken: a NetworkPolicy with the server's policy name
	// exists and belongs to someone else, so the operator wrote nothing.
	ReasonPolicyNameTaken = "PolicyNameTaken"
)

// recordPolicyEnforcement sets NetworkPolicyApplied for a policy that was just
// written, according to whether anything here will enforce it (#199).
//
// Writing a NetworkPolicy succeeds whether or not a CNI reads it, so the write
// alone is no evidence the provider's egress is restricted. True is reserved
// for an observed enforcer; a cluster with none reads False, and one the probe
// could not answer for reads Unknown -- doubt, not a claim either way. The
// Warning fires on the transition into PolicyWrittenUnenforced, not on every
// reconcile of a cluster that will never grow a CNI.
func (r *MCPServerReconciler) recordPolicyEnforcement(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, written string) {
	signal := r.EnforcementProbe.Signal(ctx)
	switch signal.Verdict {
	case networkpolicy.EnforcementObserved:
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionTrue,
			"PolicyApplied", written+"; enforcer observed: "+signal.Source)
	case networkpolicy.EnforcementNotObserved:
		msg := written + " but nothing here enforces it: " + signal.Source
		prev := getCondition(mcpServer.Status.Conditions, ConditionNetworkPolicyApplied)
		if prev == nil || prev.Reason != ReasonPolicyWrittenUnenforced {
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, "NetworkPolicyUnenforced", ActionReconcile, "%s", msg)
		}
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionFalse,
			ReasonPolicyWrittenUnenforced, msg)
	default:
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionUnknown,
			ReasonPolicyWrittenUnverified, written+"; enforcement unverified: "+signal.Source)
	}
}

// activeViolationsPrefix starts the ViolationDetected message; the active
// violation types follow it, comma-separated.
const activeViolationsPrefix = "Active violations: "

// activeViolationTypes reads the violation types a True ViolationDetected
// condition lists as active.
func activeViolationTypes(cond *metav1.Condition) map[string]bool {
	active := map[string]bool{}
	if cond == nil || cond.Status != metav1.ConditionTrue || !strings.HasPrefix(cond.Message, activeViolationsPrefix) {
		return active
	}
	for _, t := range strings.Split(strings.TrimPrefix(cond.Message, activeViolationsPrefix), ",") {
		if t = strings.TrimSpace(t); t != "" {
			active[t] = true
		}
	}
	return active
}

// reconcileViolationDetection checks for capability violations and records them.
// Violations are appended to status.Violations (capped at MaxViolationRecords).
// Does not call Status().Update() -- caller handles that.
func (r *MCPServerReconciler) reconcileViolationDetection(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	if mcpServer.Spec.Capabilities == nil {
		return nil
	}

	logger := log.FromContext(ctx)
	now := metav1.Now()
	enforcementMode := mcpServer.Spec.Capabilities.EnforcementMode
	if enforcementMode == "" {
		enforcementMode = "alert"
	}

	var newViolations []mcpv1alpha2.ViolationRecord

	// Detection 1: NetworkPolicy drift -- capabilities declare network egress
	// but NetworkPolicyApplied condition is not True. A policy that was written
	// but whose enforcement is not observed is not drift: the policy is there,
	// and the condition plus its one transition Warning already carry the
	// enforcement gap (#199). Counting it here would record a violation and a
	// Warning on every reconcile of every server on such a cluster.
	if mcpServer.Spec.Capabilities.Network != nil && len(mcpServer.Spec.Capabilities.Network.Egress) > 0 {
		npCond := getCondition(mcpServer.Status.Conditions, ConditionNetworkPolicyApplied)
		if npCond == nil || (npCond.Status != metav1.ConditionTrue &&
			npCond.Reason != ReasonPolicyWrittenUnenforced && npCond.Reason != ReasonPolicyWrittenUnverified) {
			newViolations = append(newViolations, mcpv1alpha2.ViolationRecord{
				Type:      "capability_drift",
				Detail:    "Network capabilities declared but NetworkPolicy not applied",
				Severity:  "high",
				Action:    enforcementMode,
				Timestamp: now,
			})
		}
	}

	// Detection 2: Tool count drift -- more tools than declared maximum
	if mcpServer.Spec.Capabilities.Tools != nil && mcpServer.Spec.Capabilities.Tools.MaxCount > 0 {
		if mcpServer.Status.ToolsCount > mcpServer.Spec.Capabilities.Tools.MaxCount {
			newViolations = append(newViolations, mcpv1alpha2.ViolationRecord{
				Type:      "undeclared_tool",
				Detail:    fmt.Sprintf("Provider exposes %d tools but max declared is %d", mcpServer.Status.ToolsCount, mcpServer.Spec.Capabilities.Tools.MaxCount),
				Severity:  "medium",
				Action:    enforcementMode,
				Timestamp: now,
			})
		}
	}

	if len(newViolations) == 0 {
		// Clear condition if it was previously set
		cond := getCondition(mcpServer.Status.Conditions, ConditionViolationDetected)
		if cond != nil && cond.Status == metav1.ConditionTrue {
			setServerCondition(mcpServer, ConditionViolationDetected, metav1.ConditionFalse,
				"NoViolations", "No capability violations detected")
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonViolationCleared, ActionReconcile,
				"No capability violations detected")
		}
		return nil
	}

	// Record only what is new. A condition that persists -- a NetworkPolicy
	// that stays unapplied, a tool count that stays over -- is one violation,
	// not one per reconcile: it used to append a record, bump the counter and
	// emit a Warning on every pass (#245). The active set is kept in the
	// ViolationDetected condition.
	previouslyActive := activeViolationTypes(getCondition(mcpServer.Status.Conditions, ConditionViolationDetected))
	var activeTypes []string
	var fresh []mcpv1alpha2.ViolationRecord
	for _, v := range newViolations {
		activeTypes = append(activeTypes, v.Type)
		if !previouslyActive[v.Type] {
			fresh = append(fresh, v)
		}
	}
	sort.Strings(activeTypes)
	newViolations = fresh

	// Record violations
	for _, v := range newViolations {
		logger.Info("Capability violation detected",
			"provider", mcpServer.Name,
			"type", v.Type,
			"severity", v.Severity,
			"action", v.Action,
			"detail", v.Detail,
		)
		metrics.RecordViolation(mcpServer.Namespace, mcpServer.Name, v.Type)
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonViolationDetected, ActionReconcile,
			"Capability violation: %s - %s", v.Type, v.Detail)
	}

	// Append to status, cap at MaxViolationRecords
	mcpServer.Status.Violations = append(mcpServer.Status.Violations, newViolations...)
	if len(mcpServer.Status.Violations) > mcpv1alpha2.MaxViolationRecords {
		overflow := len(mcpServer.Status.Violations) - mcpv1alpha2.MaxViolationRecords
		mcpServer.Status.Violations = mcpServer.Status.Violations[overflow:]
	}

	// Set condition
	setServerCondition(mcpServer, ConditionViolationDetected, metav1.ConditionTrue,
		"ViolationsFound", activeViolationsPrefix+strings.Join(activeTypes, ", "))

	return nil
}

// reconcileEgressAudit emits a Warning event when a provider uses wildcard egress
// (host "*", cidr 0.0.0.0/0 or ::/0) with the explicit override annotation. It
// is the audit trail for the allowed case; the validating webhook refuses the
// unannotated one.
func (r *MCPServerReconciler) reconcileEgressAudit(_ context.Context, mcpServer *mcpv1alpha2.MCPServer) {
	if mcpServer.Spec.Capabilities == nil ||
		mcpServer.Spec.Capabilities.Network == nil {
		return
	}
	for _, rule := range mcpServer.Spec.Capabilities.Network.Egress {
		if networkpolicy.IsUnrestrictedEgress(rule) {
			ann := mcpServer.GetAnnotations()
			if ann != nil && ann["hangar.io/allow-unrestricted-egress"] == "true" {
				r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonUnrestrictedEgressAllowed, ActionReconcile,
					"Provider uses wildcard egress with explicit override annotation")
			}
			return
		}
	}
}

// namespaceEnforcesEgress reports whether a namespace has opted into egress
// enforcement via the enforce-egress label (the namespace default-deny is only
// applied where this label is set -- see NamespaceEgressReconciler).
func (r *MCPServerReconciler) namespaceEnforcesEgress(ctx context.Context, name string) (bool, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &ns); err != nil {
		if errors.IsNotFound(err) {
			// Namespace not visible -> cannot confirm governance, and no
			// default-deny would exist either, so treat as not-governed.
			return false, nil
		}
		return false, fmt.Errorf("get namespace %q: %w", name, err)
	}
	return ns.Labels[networkpolicy.EnforceEgressLabel] == "true", nil
}

// deleteNetworkPolicyIfExists deletes the NetworkPolicy for a provider if it exists.
func (r *MCPServerReconciler) deleteNetworkPolicyIfExists(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) error {
	npName := networkpolicy.NetworkPolicyName(mcpServer.Name)
	existing := &networkingv1.NetworkPolicy{}
	npKey := types.NamespacedName{Name: npName, Namespace: mcpServer.Namespace}

	err := r.Get(ctx, npKey, existing)
	if errors.IsNotFound(err) {
		return nil // Nothing to delete
	} else if err != nil {
		return err
	}
	if !perServerPolicyOwned(mcpServer, existing) {
		return nil // someone else's policy under our name (#210)
	}

	return r.Delete(ctx, existing)
}

// perServerPolicyOwned reports whether the operator may rewrite or delete the
// per-server NetworkPolicy np: it is controlled by this MCPServer, or it has
// no controller and carries the operator's managed-by label (a policy written
// before owner references were set).
func perServerPolicyOwned(mcpServer *mcpv1alpha2.MCPServer, np *networkingv1.NetworkPolicy) bool {
	if metav1.IsControlledBy(np, mcpServer) {
		return true
	}
	return metav1.GetControllerOf(np) == nil &&
		np.Labels[networkpolicy.LabelManagedBy] == networkpolicy.DefaultManagerName
}

// reconcileDelete handles provider deletion
func (r *MCPServerReconciler) reconcileDelete(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling deletion for MCPServer")

	// Clean up the Deployment (its pods go with it) and a legacy fixed-name
	// pod, if container mode. Garbage collection would get there too, but not
	// before the finalizer is released.
	if mcpServer.IsContainerMode() {
		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: provider.DeploymentName(mcpServer), Namespace: mcpServer.Namespace,
		}}
		logger.Info("Deleting Deployment", "deployment", dep.Name)
		if err := r.Delete(ctx, dep); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err := r.removeLegacyPod(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Deregister from MCP-Hangar core, but only a server this operator put
	// there. A container-mode server exists in core because of the pod this
	// operator ran; a remote one only if the operator registered it.
	if r.HangarClient != nil {
		if err := r.deregisterOwned(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Clean up metrics
	metrics.ClearProviderMetrics(mcpServer.Namespace, mcpServer.Name)

	// Remove finalizer
	controllerutil.RemoveFinalizer(mcpServer, finalizerName)
	if err := r.Update(ctx, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonDeleted, ActionReconcile,
		"Provider deleted")
	logger.Info("MCPServer deleted successfully")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// A controller that writes status to the resource it watches will
		// re-trigger itself on its own writes unless something says otherwise,
		// and nothing did: an unhealthy remote server spun at 168 reconciles a
		// second, bounded only by API round-trip latency.
		//
		// Not a bare GenerationChangedPredicate, which would drop metadata
		// changes too -- the discovery annotations this operator stamps are
		// exactly that. Spec, labels and annotations still wake it; a status-only
		// update does not, and the RequeueAfter this reconciler already returns
		// is what paces the polling.
		For(&mcpv1alpha2.MCPServer{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.LabelChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		))).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Owns(&appsv1.Deployment{}).
		// The Deployment's pods belong to its ReplicaSet, not to the MCPServer,
		// so a crash would wake nothing through Owns. They carry the server's
		// name as a label (#221).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(providerPodToServer)).
		Owns(&networkingv1.NetworkPolicy{}).
		// The enforce-egress label decides whether an unpinned server's egress
		// is withheld, and it lives on the Namespace, not the MCPServer. Without
		// this watch a label flip took effect only at each server's next poll,
		// up to ten minutes later (#205).
		Watches(&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.mcpServersInNamespace),
			builder.WithPredicates(enforceEgressLabelChanged())).
		Complete(r)
}

// mcpServersInNamespace maps a Namespace to every MCPServer in it.
func (r *MCPServerReconciler) mcpServersInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	var list mcpv1alpha2.MCPServerList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetName())); err != nil {
		log.FromContext(ctx).Error(err, "list MCPServers for namespace label change", "namespace", obj.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return reqs
}

// enforceEgressLabelChanged passes only a Namespace update that changes the
// enforce-egress label. Creates are dropped: a new namespace holds no
// MCPServers yet, and each server reads the label on its own first reconcile.
func enforceEgressLabelChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return e.ObjectOld.GetLabels()[networkpolicy.EnforceEgressLabel] !=
				e.ObjectNew.GetLabels()[networkpolicy.EnforceEgressLabel]
		},
	}
}

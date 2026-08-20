// Package controller implements Kubernetes controllers for MCP resources
package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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

	// maxConsecutiveFailures caps Status.ConsecutiveFailures so the counter
	// cannot grow without bound while a provider stays unhealthy. Also used as
	// the Pod restart-backoff ceiling in handlePodFailed.
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
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     events.EventRecorder
	HangarClient *hangar.Client
	Config       *ReconcilerConfig
}

// ReconcilerConfig holds configuration for the reconciler
type ReconcilerConfig struct {
	// MaxConcurrentReconciles limits concurrent reconciliations
	MaxConcurrentReconciles int

	// ReadyRequeueInterval for ready providers
	ReadyRequeueInterval time.Duration

	// ErrorRequeueInterval for errored providers
	ErrorRequeueInterval time.Duration

	// DefaultImage for provider sidecar
	DefaultImage string
}

// DefaultReconcilerConfig returns default configuration
func DefaultReconcilerConfig() *ReconcilerConfig {
	return &ReconcilerConfig{
		MaxConcurrentReconciles: 10,
		ReadyRequeueInterval:    5 * time.Minute,
		ErrorRequeueInterval:    10 * time.Second,
		DefaultImage:            "ghcr.io/mcp-hangar/mcp-hangar-sidecar:latest",
	}
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch
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
	startTime := time.Now()

	logger.Info("Reconciling MCPServer", "namespacedName", req.NamespacedName)
	defer func() {
		duration := time.Since(startTime)
		metrics.ReconcileDuration.WithLabelValues("mcpserver").Observe(duration.Seconds())
	}()

	// Fetch the MCPServer instance
	mcpServer := &mcpv1alpha2.MCPServer{}
	if err := r.Get(ctx, req.NamespacedName, mcpServer); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPServer resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPServer")
		metrics.ReconcileTotal.WithLabelValues("mcpserver", "error").Inc()
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !mcpServer.ObjectMeta.DeletionTimestamp.IsZero() {
		result, err := r.reconcileDelete(ctx, mcpServer)
		if err != nil {
			metrics.ReconcileTotal.WithLabelValues("mcpserver", "error").Inc()
		} else {
			metrics.ReconcileTotal.WithLabelValues("mcpserver", "success").Inc()
		}
		return result, err
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(mcpServer, finalizerName) {
		controllerutil.AddFinalizer(mcpServer, finalizerName)
		if err := r.Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Main reconciliation logic
	result, err := r.reconcileNormal(ctx, mcpServer)
	if err != nil {
		metrics.ReconcileTotal.WithLabelValues("mcpserver", "error").Inc()
	} else {
		metrics.ReconcileTotal.WithLabelValues("mcpserver", "success").Inc()
	}

	return result, err
}

// reconcileNormal handles normal (non-deletion) reconciliation
func (r *MCPServerReconciler) reconcileNormal(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Update observed generation
	if mcpServer.Status.ObservedGeneration != mcpServer.Generation {
		mcpServer.Status.ObservedGeneration = mcpServer.Generation
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "Reconciling", "Processing spec changes")
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
		if err := r.Status().Update(ctx, mcpServer); err != nil {
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
		if err := r.Status().Update(ctx, mcpServer); err != nil {
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

	// Build desired Pod spec
	desiredPod, err := provider.BuildPodForMCPServer(mcpServer)
	if err != nil {
		logger.Error(err, "Failed to build Pod spec")
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "PodBuildFailed", err.Error())
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(mcpServer, desiredPod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// Check if Pod exists
	existingPod := &corev1.Pod{}
	podKey := types.NamespacedName{Name: desiredPod.Name, Namespace: desiredPod.Namespace}
	err = r.Get(ctx, podKey, existingPod)

	if errors.IsNotFound(err) {
		return r.handlePodNotFound(ctx, mcpServer, desiredPod)
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Detect spec drift: if the provider generation changed since the Pod was
	// created, delete the stale Pod and let the next reconcile recreate it.
	if r.podSpecDrifted(mcpServer, existingPod) {
		logger.Info("Provider spec changed, recreating Pod",
			"provider", mcpServer.Name,
			"podGeneration", existingPod.Annotations[provider.AnnotationGeneration],
			"providerGeneration", mcpServer.Generation)
		if err := r.Delete(ctx, existingPod); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "SpecChanged", "Provider spec changed, recreating Pod")
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonUpdated, ActionReconcile,
			"Spec changed, recreating Pod")
		metrics.MCPServerRestarts.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}

	// Pod exists - sync status
	return r.syncPodStatus(ctx, mcpServer, existingPod)
}

// handlePodNotFound handles the case when the provider Pod doesn't exist
func (r *MCPServerReconciler) handlePodNotFound(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, desiredPod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if we should create (replicas > 0)
	if mcpServer.IsCold() {
		// Cold state - don't create pod
		logger.Info("Provider is cold (replicas=0), not creating Pod")
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateCold
		mcpServer.Status.ReadyReplicas = 0
		mcpServer.Status.AvailableReplicas = 0
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "Cold", "Provider is cold, will start on demand")
		setServerCondition(mcpServer, ConditionAvailable, metav1.ConditionFalse, "Cold", "No replicas requested")

		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}

		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha2.MCPServerStateCold))
		return ctrl.Result{RequeueAfter: coldRequeueAfter}, nil
	}

	// Create Pod
	logger.Info("Creating Pod for provider", "pod", desiredPod.Name)
	if err := r.Create(ctx, desiredPod); err != nil {
		logger.Error(err, "Failed to create Pod")
		setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "PodCreateFailed", err.Error())
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
			"Failed to create Pod: %v", err)
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
	}

	// Update status
	mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
	mcpServer.Status.PodName = desiredPod.Name
	now := metav1.Now()
	mcpServer.Status.LastStartedAt = &now
	setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "PodCreated", "Pod created, waiting for ready")

	if err := r.Status().Update(ctx, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonStarting, ActionReconcile,
		"Creating provider Pod")
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha2.MCPServerStateInitializing))

	return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
}

// syncPodStatus synchronizes MCPServer status with Pod status
func (r *MCPServerReconciler) syncPodStatus(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, pod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	requeueAfter := defaultRequeueAfter

	// Map Pod phase to Provider state
	switch pod.Status.Phase {
	case corev1.PodRunning:
		requeueAfter = r.handlePodRunning(ctx, mcpServer, pod)

	case corev1.PodPending:
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateInitializing
		mcpServer.Status.ReadyReplicas = 0
		setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionTrue, "PodPending", "Pod is pending")
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Initializing")

	case corev1.PodFailed:
		requeueAfter = r.handlePodFailed(ctx, mcpServer, pod)

	case corev1.PodSucceeded:
		// Container exited cleanly - this is unusual, restart it
		logger.Info("Pod succeeded (exited cleanly), restarting")
		mcpServer.Status.State = mcpv1alpha2.MCPServerStateCold
		now := metav1.Now()
		mcpServer.Status.LastStoppedAt = &now

		if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Cold")

	default:
		logger.Info("Unknown pod phase", "phase", pod.Status.Phase)
	}

	// Update status
	mcpServer.Status.PodName = pod.Name

	// Propagate capabilities from spec to status (Phase 38).
	// Phase 39 may enrich status.capabilities with resolved IPs and computed fields.
	if mcpServer.Spec.Capabilities != nil {
		mcpServer.Status.Capabilities = mcpServer.Spec.Capabilities.DeepCopy()
	} else {
		mcpServer.Status.Capabilities = nil
	}

	if err := r.Status().Update(ctx, mcpServer); err != nil {
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

	setServerCondition(mcpServer, ConditionReady, metav1.ConditionTrue, "ProviderReady", "Provider is ready")
	setServerCondition(mcpServer, ConditionProgressing, metav1.ConditionFalse, "Reconciled", "")
	setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionFalse, "", "")
	setServerCondition(mcpServer, ConditionAvailable, metav1.ConditionTrue, "Available", "Provider is available")

	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonReady, ActionReconcile,
		"Provider is ready")
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Ready")

	return readyRequeueAfter
}

// handlePodFailed handles a failed Pod
func (r *MCPServerReconciler) handlePodFailed(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer, pod *corev1.Pod) time.Duration {
	logger := log.FromContext(ctx)

	mcpServer.Status.State = mcpv1alpha2.MCPServerStateDead
	mcpServer.Status.ConsecutiveFailures++
	mcpServer.Status.ReadyReplicas = 0
	mcpServer.Status.AvailableReplicas = 0

	// Get failure reason
	reason := "Unknown"
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			reason = cs.State.Terminated.Reason
			if cs.State.Terminated.Message != "" {
				reason = fmt.Sprintf("%s: %s", reason, cs.State.Terminated.Message)
			}
			break
		}
	}

	setServerCondition(mcpServer, ConditionReady, metav1.ConditionFalse, "PodFailed", reason)
	setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "PodFailed", reason)
	r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonFailed, ActionReconcile,
		"Pod failed: %s", reason)
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Dead")

	// Check if we should restart (with backoff)
	maxFailures := maxConsecutiveFailures
	if mcpServer.Status.ConsecutiveFailures < maxFailures {
		logger.Info("Pod failed, deleting for restart",
			"failures", mcpServer.Status.ConsecutiveFailures,
			"maxFailures", maxFailures)

		if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to delete failed Pod")
		}

		// Exponential backoff
		backoff := time.Duration(mcpServer.Status.ConsecutiveFailures) * 10 * time.Second
		return backoff
	}

	logger.Info("Max failures reached, not restarting", "failures", mcpServer.Status.ConsecutiveFailures)
	return readyRequeueAfter
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
		if err := r.Status().Update(ctx, mcpServer); err != nil {
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
		health, err := r.HangarClient.GetMCPServerHealth(ctx, mcpServer.Name, mcpServer.Namespace)
		if err != nil {
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
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "HealthCheckFailed", err.Error())
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonUnhealthy, ActionReconcile,
				"Health check failed: %v", err)
			metrics.MCPServerHealthCheckFailures.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
			// Re-probe soon so recovery is detected fast, not after the full readyRequeueAfter window.
			requeueAfter = errorRequeueAfter
		} else if health.ConsecutiveFailures == 0 {
			mcpServer.Status.State = mcpv1alpha2.MCPServerStateReady
			mcpServer.Status.ConsecutiveFailures = 0
			now := metav1.Now()
			mcpServer.Status.LastHealthCheck = &now
			setServerCondition(mcpServer, ConditionReady, metav1.ConditionTrue, "EndpointHealthy", "Remote endpoint is healthy")
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, ReasonHealthy, ActionReconcile,
				"Remote endpoint is healthy")

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
			setServerCondition(mcpServer, ConditionDegraded, metav1.ConditionTrue, "EndpointUnhealthy",
				fmt.Sprintf("Core reports %d consecutive failures (success rate %.2f)", health.ConsecutiveFailures, health.SuccessRate))
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, ReasonUnhealthy, ActionReconcile,
				"Remote endpoint unhealthy")
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

	if err := r.Status().Update(ctx, mcpServer); err != nil {
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
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create NetworkPolicy: %w", err)
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, "NetworkPolicyCreated", ActionReconcile,
			"Created NetworkPolicy %s", desired.Name)
		setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionTrue,
			"PolicyApplied", fmt.Sprintf("NetworkPolicy %s created", desired.Name))
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy: %w", err)
	}

	// Update if spec changed
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		logger.Info("Updating NetworkPolicy for provider",
			"networkPolicy", desired.Name, "provider", mcpServer.Name)
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		existing.Annotations = desired.Annotations
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update NetworkPolicy: %w", err)
		}
		r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeNormal, "NetworkPolicyUpdated", ActionReconcile,
			"Updated NetworkPolicy %s", desired.Name)
	}

	setServerCondition(mcpServer, ConditionNetworkPolicyApplied, metav1.ConditionTrue,
		"PolicyApplied", fmt.Sprintf("NetworkPolicy %s applied", desired.Name))
	return nil
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
	// but NetworkPolicyApplied condition is not True
	if mcpServer.Spec.Capabilities.Network != nil && len(mcpServer.Spec.Capabilities.Network.Egress) > 0 {
		npCond := getCondition(mcpServer.Status.Conditions, ConditionNetworkPolicyApplied)
		if npCond == nil || npCond.Status != metav1.ConditionTrue {
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
		"ViolationsFound", fmt.Sprintf("%d new violation(s) detected", len(newViolations)))

	return nil
}

// reconcileEgressAudit emits a Warning event when a provider uses wildcard egress
// with the explicit override annotation. This provides an audit trail without
// blocking admission (the CEL rule handles rejection; this covers the allowed override case).
func (r *MCPServerReconciler) reconcileEgressAudit(_ context.Context, mcpServer *mcpv1alpha2.MCPServer) {
	if mcpServer.Spec.Capabilities == nil ||
		mcpServer.Spec.Capabilities.Network == nil {
		return
	}
	for _, rule := range mcpServer.Spec.Capabilities.Network.Egress {
		if rule.Host == "*" {
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

	return r.Delete(ctx, existing)
}

// podSpecDrifted returns true if the running Pod was built from an older
// provider spec (detected via the generation annotation set by the Pod builder).
// In Kubernetes, .metadata.generation is only incremented when .spec changes,
// so finalizer or status updates do not trigger false drift.
func (r *MCPServerReconciler) podSpecDrifted(mcpServer *mcpv1alpha2.MCPServer, pod *corev1.Pod) bool {
	actual, ok := pod.Annotations[provider.AnnotationGeneration]
	if !ok {
		// Pod has no generation annotation -- was created before drift detection
		// existed. Skip to avoid infinite recreate loops.
		return false
	}
	expected := strconv.FormatInt(mcpServer.Generation, 10)
	return actual != expected
}

// reconcileDelete handles provider deletion
func (r *MCPServerReconciler) reconcileDelete(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling deletion for MCPServer")

	// Clean up Pod if container mode
	if mcpServer.IsContainerMode() {
		pod := &corev1.Pod{}
		podKey := types.NamespacedName{
			Name:      mcpServer.GetPodName(),
			Namespace: mcpServer.Namespace,
		}
		if err := r.Get(ctx, podKey, pod); err == nil {
			logger.Info("Deleting Pod", "pod", pod.Name)
			if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
	}

	// Deregister from MCP-Hangar core
	if r.HangarClient != nil {
		if err := r.HangarClient.DeregisterMCPServer(ctx, mcpServer.Name, mcpServer.Namespace); err != nil {
			logger.Error(err, "Failed to deregister provider from Hangar")
			// Continue anyway - don't block deletion
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
		Owns(&corev1.Pod{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}

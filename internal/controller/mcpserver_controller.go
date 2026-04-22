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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
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
	Recorder     record.EventRecorder
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
	mcpServer := &mcpv1alpha1.MCPServer{}
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
func (r *MCPServerReconciler) reconcileNormal(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Update observed generation
	if mcpServer.Status.ObservedGeneration != mcpServer.Generation {
		mcpServer.Status.ObservedGeneration = mcpServer.Generation
		mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionTrue, "Reconciling", "Processing spec changes")
	}

	// Route based on mode
	switch mcpServer.Spec.Mode {
	case mcpv1alpha1.MCPServerModeContainer:
		return r.reconcileContainerProvider(ctx, mcpServer)
	case mcpv1alpha1.MCPServerModeRemote:
		return r.reconcileRemoteProvider(ctx, mcpServer)
	default:
		logger.Error(nil, "Unknown provider mode", "mode", mcpServer.Spec.Mode)
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "InvalidMode", "Unknown provider mode")
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
}

// reconcileContainerProvider handles container-mode providers
func (r *MCPServerReconciler) reconcileContainerProvider(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate image is specified
	if mcpServer.Spec.Image == "" {
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "InvalidSpec", "Container mode requires image")
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateDead
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonFailed, "Container mode requires image")
		return ctrl.Result{}, nil
	}

	// Reconcile NetworkPolicy (independent of Pod lifecycle)
	if err := r.reconcileNetworkPolicy(ctx, mcpServer); err != nil {
		logger.Error(err, "Failed to reconcile NetworkPolicy")
		// Non-blocking: log error but continue with Pod reconciliation
		r.Recorder.Event(mcpServer, corev1.EventTypeWarning, "NetworkPolicyFailed",
			fmt.Sprintf("Failed to reconcile NetworkPolicy: %v", err))
	}

	// Reconcile violation detection (after NetworkPolicy, before Pod lifecycle)
	if err := r.reconcileViolationDetection(ctx, mcpServer); err != nil {
		logger.Error(err, "Failed to reconcile violation detection")
		// Non-blocking: log error but continue with Pod reconciliation
		r.Recorder.Event(mcpServer, corev1.EventTypeWarning, "ViolationDetectionFailed",
			fmt.Sprintf("Failed to detect violations: %v", err))
	}

	// Audit wildcard egress override usage (emits Warning event for audit trail)
	r.reconcileEgressAudit(ctx, mcpServer)

	// Build desired Pod spec
	desiredPod, err := provider.BuildPodForMCPServer(mcpServer)
	if err != nil {
		logger.Error(err, "Failed to build Pod spec")
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "PodBuildFailed", err.Error())
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
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateInitializing
		mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionTrue, "SpecChanged", "Provider spec changed, recreating Pod")
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonUpdated, "Spec changed, recreating Pod")
		metrics.MCPServerRestarts.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
		return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
	}

	// Pod exists - sync status
	return r.syncPodStatus(ctx, mcpServer, existingPod)
}

// handlePodNotFound handles the case when the provider Pod doesn't exist
func (r *MCPServerReconciler) handlePodNotFound(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, desiredPod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if we should create (replicas > 0)
	if mcpServer.IsCold() {
		// Cold state - don't create pod
		logger.Info("Provider is cold (replicas=0), not creating Pod")
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateCold
		mcpServer.Status.ReadyReplicas = 0
		mcpServer.Status.AvailableReplicas = 0
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "Cold", "Provider is cold, will start on demand")
		mcpServer.Status.SetCondition(ConditionAvailable, metav1.ConditionFalse, "Cold", "No replicas requested")

		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}

		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha1.MCPServerStateCold))
		return ctrl.Result{RequeueAfter: coldRequeueAfter}, nil
	}

	// Create Pod
	logger.Info("Creating Pod for provider", "pod", desiredPod.Name)
	if err := r.Create(ctx, desiredPod); err != nil {
		logger.Error(err, "Failed to create Pod")
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "PodCreateFailed", err.Error())
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateDead
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonFailed, fmt.Sprintf("Failed to create Pod: %v", err))
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
	}

	// Update status
	mcpServer.Status.State = mcpv1alpha1.MCPServerStateInitializing
	mcpServer.Status.PodName = desiredPod.Name
	now := metav1.Now()
	mcpServer.Status.LastStartedAt = &now
	mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionTrue, "PodCreated", "Pod created, waiting for ready")

	if err := r.Status().Update(ctx, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonStarting, "Creating provider Pod")
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, string(mcpv1alpha1.MCPServerStateInitializing))

	return ctrl.Result{RequeueAfter: defaultRequeueAfter}, nil
}

// syncPodStatus synchronizes MCPServer status with Pod status
func (r *MCPServerReconciler) syncPodStatus(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, pod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	requeueAfter := defaultRequeueAfter

	// Map Pod phase to Provider state
	switch pod.Status.Phase {
	case corev1.PodRunning:
		requeueAfter = r.handlePodRunning(ctx, mcpServer, pod)

	case corev1.PodPending:
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateInitializing
		mcpServer.Status.ReadyReplicas = 0
		mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionTrue, "PodPending", "Pod is pending")
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Initializing")

	case corev1.PodFailed:
		requeueAfter = r.handlePodFailed(ctx, mcpServer, pod)

	case corev1.PodSucceeded:
		// Container exited cleanly - this is unusual, restart it
		logger.Info("Pod succeeded (exited cleanly), restarting")
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateCold
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
func (r *MCPServerReconciler) handlePodRunning(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, pod *corev1.Pod) time.Duration {
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
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateInitializing
		mcpServer.Status.ReadyReplicas = 0
		mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionTrue, "ContainersStarting", "Waiting for containers to be ready")
		metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Initializing")
		return defaultRequeueAfter
	}

	// All containers ready - probe MCP-Hangar for tools
	if r.HangarClient != nil {
		tools, err := r.HangarClient.GetMCPServerTools(ctx, mcpServer.Name, mcpServer.Namespace)
		if err != nil {
			logger.Error(err, "Failed to get provider tools from Hangar")
			mcpServer.Status.State = mcpv1alpha1.MCPServerStateDegraded
			mcpServer.Status.ConsecutiveFailures++
			mcpServer.Status.SetCondition(ConditionDegraded, metav1.ConditionTrue, "ToolsFetchFailed", err.Error())
			metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Degraded")
			metrics.MCPServerHealthCheckFailures.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
			return defaultRequeueAfter
		}

		mcpServer.Status.Tools = tools
		mcpServer.Status.ToolsCount = int32(len(tools))
		metrics.MCPServerToolsCount.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Set(float64(len(tools)))
	}

	// Provider is ready
	mcpServer.Status.State = mcpv1alpha1.MCPServerStateReady
	mcpServer.Status.ReadyReplicas = 1
	mcpServer.Status.AvailableReplicas = 1
	mcpServer.Status.ConsecutiveFailures = 0
	now := metav1.Now()
	mcpServer.Status.LastHealthCheck = &now

	mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionTrue, "ProviderReady", "Provider is ready")
	mcpServer.Status.SetCondition(ConditionProgressing, metav1.ConditionFalse, "Reconciled", "")
	mcpServer.Status.SetCondition(ConditionDegraded, metav1.ConditionFalse, "", "")
	mcpServer.Status.SetCondition(ConditionAvailable, metav1.ConditionTrue, "Available", "Provider is available")

	r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonReady, "Provider is ready")
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Ready")

	return readyRequeueAfter
}

// handlePodFailed handles a failed Pod
func (r *MCPServerReconciler) handlePodFailed(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, pod *corev1.Pod) time.Duration {
	logger := log.FromContext(ctx)

	mcpServer.Status.State = mcpv1alpha1.MCPServerStateDead
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

	mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "PodFailed", reason)
	mcpServer.Status.SetCondition(ConditionDegraded, metav1.ConditionTrue, "PodFailed", reason)
	r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonFailed, fmt.Sprintf("Pod failed: %s", reason))
	metrics.SetMCPServerState(mcpServer.Namespace, mcpServer.Name, "Dead")

	// Check if we should restart (with backoff)
	maxFailures := int32(5)
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
func (r *MCPServerReconciler) reconcileRemoteProvider(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate endpoint
	endpoint := mcpServer.Spec.Endpoint
	if endpoint == "" {
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionFalse, "NoEndpoint", "Remote provider requires endpoint")
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateDead
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Health check via MCP-Hangar core (if client available)
	if r.HangarClient != nil {
		healthy, tools, err := r.HangarClient.HealthCheckRemote(ctx, endpoint)
		if err != nil {
			logger.Error(err, "Remote health check failed")
			mcpServer.Status.State = mcpv1alpha1.MCPServerStateDegraded
			mcpServer.Status.ConsecutiveFailures++
			mcpServer.Status.SetCondition(ConditionDegraded, metav1.ConditionTrue, "HealthCheckFailed", err.Error())
			r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonUnhealthy, fmt.Sprintf("Health check failed: %v", err))
			metrics.MCPServerHealthCheckFailures.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Inc()
		} else if healthy {
			mcpServer.Status.State = mcpv1alpha1.MCPServerStateReady
			mcpServer.Status.Tools = tools
			mcpServer.Status.ToolsCount = int32(len(tools))
			mcpServer.Status.ConsecutiveFailures = 0
			now := metav1.Now()
			mcpServer.Status.LastHealthCheck = &now
			mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionTrue, "EndpointHealthy", "Remote endpoint is healthy")
			r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonHealthy, "Remote endpoint is healthy")
			metrics.MCPServerToolsCount.WithLabelValues(mcpServer.Namespace, mcpServer.Name).Set(float64(len(tools)))
		} else {
			mcpServer.Status.State = mcpv1alpha1.MCPServerStateDegraded
			mcpServer.Status.ConsecutiveFailures++
			mcpServer.Status.SetCondition(ConditionDegraded, metav1.ConditionTrue, "EndpointUnhealthy", "Remote endpoint failed health check")
			r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonUnhealthy, "Remote endpoint unhealthy")
		}
	} else {
		// No Hangar client - just mark as ready (assume healthy)
		mcpServer.Status.State = mcpv1alpha1.MCPServerStateReady
		mcpServer.Status.SetCondition(ConditionReady, metav1.ConditionTrue, "Assumed", "No Hangar client, assuming healthy")
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

	return ctrl.Result{RequeueAfter: readyRequeueAfter}, nil
}

// reconcileNetworkPolicy ensures the NetworkPolicy for a provider matches its capabilities.
// Creates, updates, or deletes the NetworkPolicy as needed.
func (r *MCPServerReconciler) reconcileNetworkPolicy(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)

	desired := networkpolicy.BuildNetworkPolicy(mcpServer)

	if desired == nil {
		// No capabilities declared -- delete existing policy if any, clear condition
		if err := r.deleteNetworkPolicyIfExists(ctx, mcpServer); err != nil {
			return err
		}
		mcpServer.Status.SetCondition(ConditionNetworkPolicyApplied, metav1.ConditionFalse,
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
		r.Recorder.Event(mcpServer, corev1.EventTypeNormal, "NetworkPolicyCreated",
			fmt.Sprintf("Created NetworkPolicy %s", desired.Name))
		mcpServer.Status.SetCondition(ConditionNetworkPolicyApplied, metav1.ConditionTrue,
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
		r.Recorder.Event(mcpServer, corev1.EventTypeNormal, "NetworkPolicyUpdated",
			fmt.Sprintf("Updated NetworkPolicy %s", desired.Name))
	}

	mcpServer.Status.SetCondition(ConditionNetworkPolicyApplied, metav1.ConditionTrue,
		"PolicyApplied", fmt.Sprintf("NetworkPolicy %s applied", desired.Name))
	return nil
}

// reconcileViolationDetection checks for capability violations and records them.
// Violations are appended to status.Violations (capped at MaxViolationRecords).
// Does not call Status().Update() -- caller handles that.
func (r *MCPServerReconciler) reconcileViolationDetection(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	if mcpServer.Spec.Capabilities == nil {
		return nil
	}

	logger := log.FromContext(ctx)
	now := metav1.Now()
	enforcementMode := mcpServer.Spec.Capabilities.EnforcementMode
	if enforcementMode == "" {
		enforcementMode = "alert"
	}

	var newViolations []mcpv1alpha1.ViolationRecord

	// Detection 1: NetworkPolicy drift -- capabilities declare network egress
	// but NetworkPolicyApplied condition is not True
	if mcpServer.Spec.Capabilities.Network != nil && len(mcpServer.Spec.Capabilities.Network.Egress) > 0 {
		npCond := mcpServer.Status.GetCondition(ConditionNetworkPolicyApplied)
		if npCond == nil || npCond.Status != metav1.ConditionTrue {
			newViolations = append(newViolations, mcpv1alpha1.ViolationRecord{
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
			newViolations = append(newViolations, mcpv1alpha1.ViolationRecord{
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
		cond := mcpServer.Status.GetCondition(ConditionViolationDetected)
		if cond != nil && cond.Status == metav1.ConditionTrue {
			mcpServer.Status.SetCondition(ConditionViolationDetected, metav1.ConditionFalse,
				"NoViolations", "No capability violations detected")
			r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonViolationCleared,
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
		r.Recorder.Event(mcpServer, corev1.EventTypeWarning, ReasonViolationDetected,
			fmt.Sprintf("Capability violation: %s - %s", v.Type, v.Detail))
	}

	// Append to status, cap at MaxViolationRecords
	mcpServer.Status.Violations = append(mcpServer.Status.Violations, newViolations...)
	if len(mcpServer.Status.Violations) > mcpv1alpha1.MaxViolationRecords {
		overflow := len(mcpServer.Status.Violations) - mcpv1alpha1.MaxViolationRecords
		mcpServer.Status.Violations = mcpServer.Status.Violations[overflow:]
	}

	// Set condition
	mcpServer.Status.SetCondition(ConditionViolationDetected, metav1.ConditionTrue,
		"ViolationsFound", fmt.Sprintf("%d new violation(s) detected", len(newViolations)))

	return nil
}

// reconcileEgressAudit emits a Warning event when a provider uses wildcard egress
// with the explicit override annotation. This provides an audit trail without
// blocking admission (the CEL rule handles rejection; this covers the allowed override case).
func (r *MCPServerReconciler) reconcileEgressAudit(_ context.Context, mcpServer *mcpv1alpha1.MCPServer) {
	if mcpServer.Spec.Capabilities == nil ||
		mcpServer.Spec.Capabilities.Network == nil {
		return
	}
	for _, rule := range mcpServer.Spec.Capabilities.Network.Egress {
		if rule.Host == "*" {
			ann := mcpServer.GetAnnotations()
			if ann != nil && ann["hangar.io/allow-unrestricted-egress"] == "true" {
				r.Recorder.Event(mcpServer, corev1.EventTypeWarning,
					ReasonUnrestrictedEgressAllowed,
					"Provider uses wildcard egress with explicit override annotation")
			}
			return
		}
	}
}

// deleteNetworkPolicyIfExists deletes the NetworkPolicy for a provider if it exists.
func (r *MCPServerReconciler) deleteNetworkPolicyIfExists(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
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
func (r *MCPServerReconciler) podSpecDrifted(mcpServer *mcpv1alpha1.MCPServer, pod *corev1.Pod) bool {
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
func (r *MCPServerReconciler) reconcileDelete(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (ctrl.Result, error) {
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

	r.Recorder.Event(mcpServer, corev1.EventTypeNormal, ReasonDeleted, "Provider deleted")
	logger.Info("MCPServer deleted successfully")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServer{}).
		Owns(&corev1.Pod{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}

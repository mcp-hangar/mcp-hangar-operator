// Package controller implements Kubernetes controllers for MCP resources
package controller

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/metrics"
)

// MCPServerGroupReconciler reconciles a MCPServerGroup object.
// It is a read-only aggregation controller: it selects MCPServers by label
// selector, counts provider states, evaluates health policy thresholds, and
// reports Ready/Degraded/Available conditions on the group status subresource.
type MCPServerGroupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservergroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservergroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservergroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile performs the reconciliation loop for MCPServerGroup
func (r *MCPServerGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Reconciling MCPServerGroup", "namespacedName", req.NamespacedName)

	// Fetch the MCPServerGroup instance
	group := &mcpv1alpha2.MCPServerGroup{}
	if err := r.Get(ctx, req.NamespacedName, group); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPServerGroup resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPServerGroup")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !group.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, group)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(group, finalizerName) {
		controllerutil.AddFinalizer(group, finalizerName)
		if err := r.Update(ctx, group); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Main reconciliation logic
	return r.reconcileNormal(ctx, group)
}

// reconcileNormal handles normal (non-deletion) reconciliation for groups
func (r *MCPServerGroupReconciler) reconcileNormal(ctx context.Context, group *mcpv1alpha2.MCPServerGroup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// original snapshots the object exactly as read from the cache, before
	// any of the status mutations below, and serves two purposes:
	//
	//  1. Comparing original.Status against the final group.Status lets
	//     updateStatus skip the write entirely when a reconcile didn't
	//     actually change anything. Every member's health-check/status event
	//     maps to a Group reconcile (see findGroupsForMCPServer), so at N
	//     members a single settle window can produce many reconciles that
	//     all recompute the *same* aggregate; without this guard, each one
	//     would still issue its own status write.
	//  2. It is the patch base for the status merge-patch in updateStatus,
	//     which sends only the fields this reconcile actually changed instead
	//     of a full-object Update() -- so a reconcile that changes one counter
	//     does not rewrite the whole status.
	//
	// (1) is the fix for #32: full-object Update() calls each raced the
	// informer cache -- which can briefly lag behind this controller's own
	// prior write -- and produced a self-sustaining "object has been modified"
	// conflict storm proportional to member count. Skipping the write when
	// nothing changed is what ended that storm, and it still does.
	//
	// The patch itself carries a resourceVersion precondition (see
	// updateStatus). It has to: a diff taken against a stale base is only
	// correct if the live object still matches that base. Without the
	// precondition, any field where base and new agree is left out of the
	// patch and keeps whatever the *live* object has -- so a status could end
	// up a mixture of two reconciles, with per-state counters summing to more
	// than the member count and the member list from neither.
	original := group.DeepCopy()

	// Update observed generation if changed
	group.Status.ObservedGeneration = group.Generation

	// Convert label selector.
	// defense-in-depth: unreachable while the CRD schema enforces spec.selector
	// via +kubebuilder:validation:Required, so a persisted group always has a
	// non-nil selector. Kept as a guard against direct-cache manipulation.
	if group.Spec.Selector == nil {
		setGroupCondition(group, ConditionReady, metav1.ConditionUnknown, "NoSelector", "No label selector defined")
		if err := r.updateStatus(ctx, group, original); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(group.Spec.Selector)
	if err != nil {
		logger.Error(err, "Failed to parse label selector")
		setGroupCondition(group, ConditionReady, metav1.ConditionFalse, "InvalidSelector", fmt.Sprintf("Invalid label selector: %v", err))
		if err := r.updateStatus(ctx, group, original); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// List MCPServers matching selector in same namespace
	providerList := &mcpv1alpha2.MCPServerList{}
	listOpts := &client.ListOptions{
		Namespace:     group.Namespace,
		LabelSelector: selector,
	}
	if err := r.List(ctx, providerList, listOpts); err != nil {
		logger.Error(err, "Failed to list MCPServers")
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, err
	}

	// Aggregate status counts
	var readyCount, degradedCount, coldCount, deadCount int32
	memberStatuses := make([]mcpv1alpha2.MCPServerMemberStatus, 0, len(providerList.Items))

	for i := range providerList.Items {
		p := &providerList.Items[i]
		member := mcpv1alpha2.MCPServerMemberStatus{
			Name:            p.Name,
			Namespace:       p.Namespace,
			State:           string(p.Status.State),
			LastHealthCheck: p.Status.LastHealthCheck,
		}
		memberStatuses = append(memberStatuses, member)

		switch p.Status.State {
		case mcpv1alpha2.MCPServerStateReady:
			readyCount++
		case mcpv1alpha2.MCPServerStateDegraded:
			degradedCount++
		case mcpv1alpha2.MCPServerStateCold:
			coldCount++
		case mcpv1alpha2.MCPServerStateDead:
			deadCount++
		default:
			// Initializing or empty state treated as cold
			coldCount++
		}
	}

	// Populate status fields
	group.Status.ProviderCount = int32(len(providerList.Items))
	group.Status.ReadyCount = readyCount
	group.Status.DegradedCount = degradedCount
	group.Status.ColdCount = coldCount
	group.Status.DeadCount = deadCount
	group.Status.Providers = memberStatuses

	// Evaluate conditions
	r.evaluateConditions(group)

	// Update metrics (cheap gauge sets; always kept current regardless of
	// whether the status subresource write below is skipped as a no-op)
	metrics.GroupMCPServerCount.WithLabelValues(group.Namespace, group.Name, "Ready").Set(float64(readyCount))
	metrics.GroupMCPServerCount.WithLabelValues(group.Namespace, group.Name, "Degraded").Set(float64(degradedCount))
	metrics.GroupMCPServerCount.WithLabelValues(group.Namespace, group.Name, "Cold").Set(float64(coldCount))
	metrics.GroupMCPServerCount.WithLabelValues(group.Namespace, group.Name, "Dead").Set(float64(deadCount))

	// Update status subresource (no-op skipped, written via conflict-tolerant patch)
	if err := r.updateStatus(ctx, group, original); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("MCPServerGroup reconciled",
		"providerCount", group.Status.ProviderCount,
		"readyCount", readyCount,
		"degradedCount", degradedCount,
		"coldCount", coldCount,
		"deadCount", deadCount,
	)

	return ctrl.Result{RequeueAfter: readyRequeueAfter}, nil
}

// updateStatus writes group's status subresource, but only when it actually
// differs from original.Status, and via a JSON merge-patch computed against
// original rather than a full-object Update().
//
// A full-object Update() requires the resourceVersion sent by the client to
// still match the one on the server; under member-driven reconcile fan-in
// (see findGroupsForMCPServer) many reconciles for the same Group can be
// in flight in close succession, and each one's cached Get() can briefly lag
// behind this very controller's own prior write. That combination is what
// produced the 1000+ "object has been modified" conflict storm in #32 at
// 30-member scale -- a single conflicting retry is normal, but the storm was
// self-sustaining and proportional to member count.
//
// The precondition is not optional, and being the sole writer does not remove
// the need for it. A merge patch carries only the fields that differ between
// the base and the new object, so every field where they agree is left to
// whatever the live object holds. When the base is a cached read that lags this
// controller's own previous write, "agrees with the base" and "agrees with
// reality" are different questions, and the patch answers the wrong one.
//
// The result is a status assembled from two reconciles: per-state counters
// summing to more than providerCount, and a member list belonging to neither.
// It was reproducible under `go test -race`, which slows the reconciler enough
// to widen the window, and it is why the aggregation tests flaked.
//
// MergeFromWithOptimisticLock adds metadata.resourceVersion to the patch, so a
// stale base is rejected with 409 instead of silently merging. The conflict is
// counted below and the reconcile requeues with a fresh read. The write-skip in
// updateStatus is what keeps those conflicts rare -- it is the part that fixed
// the #32 storm, and it is untouched.
func (r *MCPServerGroupReconciler) updateStatus(ctx context.Context, group *mcpv1alpha2.MCPServerGroup, original *mcpv1alpha2.MCPServerGroup) error {
	logger := log.FromContext(ctx)

	if apiequality.Semantic.DeepEqual(original.Status, group.Status) {
		metrics.GroupStatusWriteTotal.WithLabelValues(group.Namespace, group.Name, "skipped").Inc()
		return nil
	}

	if err := r.Status().Patch(ctx, group, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		if errors.IsConflict(err) {
			metrics.GroupStatusWriteTotal.WithLabelValues(group.Namespace, group.Name, "conflict").Inc()
		} else {
			metrics.GroupStatusWriteTotal.WithLabelValues(group.Namespace, group.Name, "error").Inc()
		}
		logger.Error(err, "Failed to update MCPServerGroup status")
		return err
	}

	metrics.GroupStatusWriteTotal.WithLabelValues(group.Namespace, group.Name, "success").Inc()
	return nil
}

// evaluateConditions sets Ready, Degraded, and Available conditions based on
// provider counts and health policy thresholds.
func (r *MCPServerGroupReconciler) evaluateConditions(group *mcpv1alpha2.MCPServerGroup) {
	status := &group.Status

	// Zero-member groups
	if status.ProviderCount == 0 {
		setGroupCondition(group, ConditionReady, metav1.ConditionUnknown, "NoProviders", "No providers match selector")
		setGroupCondition(group, ConditionAvailable, metav1.ConditionFalse, "NoProviders", "No providers match selector")
		setGroupCondition(group, ConditionDegraded, metav1.ConditionFalse, "NoProviders", "No providers match selector")
		return
	}

	// Available: at least 1 ready provider can serve traffic
	if status.ReadyCount > 0 {
		setGroupCondition(group, ConditionAvailable, metav1.ConditionTrue, "ProvidersAvailable",
			fmt.Sprintf("%d provider(s) available", status.ReadyCount))
	} else {
		setGroupCondition(group, ConditionAvailable, metav1.ConditionFalse, "NoReadyProviders", "No ready providers")
	}

	// Ready: health policy threshold met via IsHealthy helper
	if status.IsHealthy(group.Spec.HealthPolicy) {
		setGroupCondition(group, ConditionReady, metav1.ConditionTrue, "HealthyThresholdMet",
			fmt.Sprintf("%d/%d providers ready", status.ReadyCount, status.ProviderCount))
	} else {
		setGroupCondition(group, ConditionReady, metav1.ConditionFalse, "HealthyThresholdNotMet",
			fmt.Sprintf("%d/%d providers ready, threshold not met", status.ReadyCount, status.ProviderCount))
	}

	// Degraded: any unhealthy providers exist (can coexist with Ready)
	unhealthyCount := status.DegradedCount + status.DeadCount
	if unhealthyCount > 0 {
		setGroupCondition(group, ConditionDegraded, metav1.ConditionTrue, "UnhealthyProviders",
			fmt.Sprintf("%d provider(s) unhealthy (%d degraded, %d dead)", unhealthyCount, status.DegradedCount, status.DeadCount))
	} else {
		setGroupCondition(group, ConditionDegraded, metav1.ConditionFalse, "AllHealthy", "All providers healthy")
	}
}

// reconcileDelete handles group deletion by cleaning up the finalizer and metrics
func (r *MCPServerGroupReconciler) reconcileDelete(ctx context.Context, group *mcpv1alpha2.MCPServerGroup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling deletion for MCPServerGroup")

	// Clear group metrics
	metrics.ClearGroupMetrics(group.Namespace, group.Name)

	// Remove finalizer
	controllerutil.RemoveFinalizer(group, finalizerName)
	if err := r.Update(ctx, group); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(group, nil, "Normal", ReasonDeleted, ActionReconcile,
		"Provider group deleted")
	logger.Info("MCPServerGroup deleted successfully")

	return ctrl.Result{}, nil
}

// findGroupsForMCPServer returns reconcile requests for all MCPServerGroups
// whose label selector matches the given MCPServer. This ensures groups
// re-reconcile when matching providers change state.
func (r *MCPServerGroupReconciler) findGroupsForMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)

	provider, ok := obj.(*mcpv1alpha2.MCPServer)
	if !ok {
		return nil
	}

	// List all groups in the provider's namespace
	groupList := &mcpv1alpha2.MCPServerGroupList{}
	if err := r.List(ctx, groupList, client.InNamespace(provider.Namespace)); err != nil {
		logger.Error(err, "Failed to list MCPServerGroups for provider mapping")
		return nil
	}

	var requests []reconcile.Request
	providerLabels := labels.Set(provider.Labels)

	for i := range groupList.Items {
		group := &groupList.Items[i]

		if group.Spec.Selector == nil {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(group.Spec.Selector)
		if err != nil {
			logger.Error(err, "Failed to parse group selector", "group", group.Name)
			continue
		}

		if selector.Matches(providerLabels) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      group.Name,
					Namespace: group.Namespace,
				},
			})
		}
	}

	return requests
}

// groupSelfWatchPredicate controls which updates to the Group object itself
// re-trigger a reconcile (member-driven reconciles via the Watches() clause
// below are unaffected by this predicate).
//
// It passes generation-changing updates (spec edits) and deletion-marking
// updates (metadata.deletionTimestamp being set) through unconditionally, but
// drops pure status-only updates. Without it, this controller's own status
// write re-triggers its own watch (For() observes the full object, status
// subresource included), which is a self-sustaining loop: write -> watch
// event -> reconcile -> write. That loop is what kept the #32 conflict storm
// growing even after member churn quieted down, and it only stopped once the
// Group was deleted.
//
// GenerationChangedPredicate alone would also (incorrectly) drop the
// deletion-marking update, since setting deletionTimestamp is a metadata
// change that does not bump Generation -- which would break finalizer-based
// deletion. predicate.Or keeps both cases working.
var groupSelfWatchPredicate = predicate.Or(
	predicate.GenerationChangedPredicate{},
	predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectNew.GetDeletionTimestamp() != nil
		},
	},
)

// SetupWithManager sets up the controller with the Manager
func (r *MCPServerGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&mcpv1alpha2.MCPServerGroup{},
			builder.WithPredicates(groupSelfWatchPredicate),
		).
		Watches(
			&mcpv1alpha2.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.findGroupsForMCPServer),
		).
		Complete(r)
}

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// Condition types for MCPEgressPolicy status.
const (
	// EgressPolicyConditionCompiled reports whether the policy was structurally compiled.
	EgressPolicyConditionCompiled = "Compiled"
	// EgressPolicyConditionBackstopApplied reports whether the L3/L4 network backstop is in place.
	EgressPolicyConditionBackstopApplied = "BackstopApplied"
	// EgressPolicyConditionDegraded reports a degraded/at-risk state (e.g. FailOpenRisk,
	// unenforceable FQDN upstreams).
	EgressPolicyConditionDegraded = "Degraded"
)

// targetNotFoundRequeueAfter is how long to wait before re-checking a policy
// whose target does not (yet) exist.
const targetNotFoundRequeueAfter = 30 // seconds

// l7PolicyFinalizer guards deletion so the compiled L7 policy is cleared from
// core before the MCPEgressPolicy object goes away. Only added when core
// integration is enabled (HangarClient set).
const l7PolicyFinalizer = "mcp-hangar.io/l7-policy-cleanup"

// MCPEgressPolicyReconciler reconciles an MCPEgressPolicy into its L3/L4 network
// backstop (a NetworkPolicy or CiliumNetworkPolicy) and, when core integration
// is enabled, pushes the compiled L7 policy (tool + argument rules) to the core
// so it is enforced at the tool-invocation chokepoint.
type MCPEgressPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// HangarClient talks to the core REST API to deliver the L7 policy. Nil when
	// core integration is disabled (no --hangar-url); L7 push is then skipped.
	HangarClient *hangar.Client
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservergroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;patch;delete

// Reconcile ensures the network backstop for a policy matches its spec, then
// records the outcome in status.
func (r *MCPEgressPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &mcpv1alpha2.MCPEgressPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: clear the L7 policy from core, then drop the finalizer.
	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, policy)
	}

	// Ensure the cleanup finalizer once core integration is on, so whatever we
	// push to core is cleared on delete.
	if r.HangarClient != nil && !controllerutil.ContainsFinalizer(policy, l7PolicyFinalizer) {
		controllerutil.AddFinalizer(policy, l7PolicyFinalizer)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	result, reconcileErr := r.reconcile(ctx, policy)

	policy.Status.ObservedGeneration = policy.Generation
	if err := r.Status().Update(ctx, policy); err != nil {
		if reconcileErr == nil {
			reconcileErr = err
		}
	}
	return result, reconcileErr
}

// reconcile does the work and mutates policy.Status conditions; the caller
// persists status.
func (r *MCPEgressPolicyReconciler) reconcile(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Backstop generation opted out: remove any backstop, report not-applied.
	if policy.Spec.NetworkBackstop != nil && !policy.Spec.NetworkBackstop.Generate {
		if err := r.deleteBackstopIfExists(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionTrue, "Compiled", "Policy compiled")
		r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionFalse,
			"BackstopGenerationDisabled", "spec.networkBackstop.generate is false")
		r.clearDegraded(policy)
		return ctrl.Result{}, nil
	}

	// Resolve the target (MCPServer or MCPServerGroup) to a pod selector.
	selector, targetName, done, err := r.resolveTargetSelector(ctx, policy)
	if err != nil {
		return ctrl.Result{}, err
	}
	if done != nil {
		// A terminal condition was set (kind unsupported / target missing).
		if err := r.deleteBackstopIfExists(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return *done, nil
	}

	if err := r.applyFlavoredBackstop(ctx, logger, policy, selector, targetName); err != nil {
		return ctrl.Result{}, err
	}

	// Deliver the compiled L7 policy to core so it is enforced at the
	// tool-invocation chokepoint (skipped when core integration is off).
	if err := r.pushL7Policy(ctx, logger, policy, selector); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// handleDeletion clears the L7 policy from core for the policy's targets and
// removes the finalizer.
func (r *MCPEgressPolicyReconciler) handleDeletion(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, l7PolicyFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.HangarClient != nil {
		// Best-effort resolve the targets; if they are already gone core will
		// have dropped their policy with the server, so an empty set is fine.
		selector, _, done, err := r.resolveTargetSelector(ctx, policy)
		if err != nil {
			return ctrl.Result{}, err
		}
		if done == nil {
			logger := log.FromContext(ctx)
			for _, name := range providerNamesFromSelector(selector) {
				if err := r.HangarClient.ClearL7Policy(ctx, name); err != nil {
					// Best-effort: a persistent clear failure (core unreachable or an auth
					// error) must NOT wedge deletion -- returning an error here requeues
					// forever and the MCPEgressPolicy is stuck Terminating until someone
					// hand-edits the finalizer. core drops a server's L7 policy when the
					// server itself is removed, and an orphaned policy on a live server is
					// overwritten by the next push, so releasing the finalizer is safe.
					r.Recorder.Event(policy, corev1.EventTypeWarning, "L7ClearFailed",
						fmt.Sprintf("Best-effort clear of L7 policy for %q failed; removing finalizer anyway: %v", name, err))
					logger.Error(err, "L7 policy clear failed; releasing finalizer to avoid blocking deletion", "server", name)
				}
			}
		}
	}
	controllerutil.RemoveFinalizer(policy, l7PolicyFinalizer)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// pushL7Policy compiles the policy's L7 rules and delivers them to core for each
// target server. A push failure requeues (so a transient core outage retries).
func (r *MCPEgressPolicyReconciler) pushL7Policy(ctx context.Context, logger logr.Logger, policy *mcpv1alpha2.MCPEgressPolicy, selector metav1.LabelSelector) error {
	if r.HangarClient == nil {
		return nil
	}
	payload := compileL7Policy(policy)
	for _, name := range providerNamesFromSelector(selector) {
		if err := r.HangarClient.SetL7Policy(ctx, name, payload); err != nil {
			r.Recorder.Event(policy, corev1.EventTypeWarning, "L7PushFailed",
				fmt.Sprintf("Failed to deliver L7 policy to core for %q: %v", name, err))
			return fmt.Errorf("push L7 policy for %q: %w", name, err)
		}
		logger.Info("Delivered L7 policy to core", "policy", policy.Name, "server", name)
	}
	return nil
}

// providerNamesFromSelector extracts the concrete provider (server) names a
// backstop selector targets -- a single MatchLabels value or the In-set of a
// MatchExpressions requirement on the provider label.
func providerNamesFromSelector(sel metav1.LabelSelector) []string {
	if v, ok := sel.MatchLabels[networkpolicy.LabelProvider]; ok {
		return []string{v}
	}
	for _, e := range sel.MatchExpressions {
		if e.Key == networkpolicy.LabelProvider && e.Operator == metav1.LabelSelectorOpIn {
			return e.Values
		}
	}
	return nil
}

// compileL7Policy flattens an MCPEgressPolicy's per-upstream tool/argument rules
// into the single L7 policy the core enforces per server: the union of the
// upstreams' allow/deny/require-approval globs and secret-pattern groups, the
// most restrictive (smallest) payload limit, and the spec's default action.
func compileL7Policy(policy *mcpv1alpha2.MCPEgressPolicy) *hangar.L7PolicyPayload {
	var allow, deny, approval, secrets []string
	var maxBytes *int64
	for i := range policy.Spec.Upstreams {
		u := policy.Spec.Upstreams[i]
		if u.Tools != nil {
			allow = appendUnique(allow, u.Tools.Allow...)
			deny = appendUnique(deny, u.Tools.Deny...)
			approval = appendUnique(approval, u.Tools.RequireApproval...)
		}
		if u.Arguments != nil && u.Arguments.Deny != nil {
			secrets = appendUnique(secrets, u.Arguments.Deny.SecretPatterns...)
			if b := u.Arguments.Deny.MaxPayloadBytes; b > 0 && (maxBytes == nil || b < *maxBytes) {
				v := b
				maxBytes = &v
			}
		}
	}
	defaultAction := string(policy.Spec.DefaultAction)
	if defaultAction == "" {
		defaultAction = string(mcpv1alpha2.EgressActionDeny)
	}
	return &hangar.L7PolicyPayload{
		Tools:         hangar.L7ToolRules{Allow: allow, Deny: deny, RequireApproval: approval},
		Arguments:     hangar.L7ArgumentRules{SecretPatterns: secrets, MaxPayloadBytes: maxBytes},
		DefaultAction: defaultAction,
	}
}

// appendUnique appends values not already present, preserving order.
func appendUnique(dst []string, values ...string) []string {
	for _, v := range values {
		found := false
		for _, existing := range dst {
			if existing == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

// resolveTargetSelector resolves the policy's targetRef to a pod selector for
// the backstop. An MCPServer resolves to its single provider label; an
// MCPServerGroup resolves to a set of member providers (provider In [...]).
//
// When it returns a non-nil *ctrl.Result, the target could not be resolved (a
// terminal status condition has already been set) and the caller should return
// that result without applying a backstop.
func (r *MCPEgressPolicyReconciler) resolveTargetSelector(
	ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy,
) (metav1.LabelSelector, string, *ctrl.Result, error) {
	name := policy.Spec.TargetRef.Name
	switch policy.Spec.TargetRef.Kind {
	case "MCPServer":
		target := &mcpv1alpha2.MCPServer{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: policy.Namespace}, target); err != nil {
			if apierrors.IsNotFound(err) {
				return metav1.LabelSelector{}, "", r.targetNotFound(policy, fmt.Sprintf("MCPServer %q not found", name)), nil
			}
			return metav1.LabelSelector{}, "", nil, err
		}
		sel := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: target.Name}}
		return sel, target.Name, nil, nil

	case "MCPServerGroup":
		group := &mcpv1alpha2.MCPServerGroup{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: policy.Namespace}, group); err != nil {
			if apierrors.IsNotFound(err) {
				return metav1.LabelSelector{}, "", r.targetNotFound(policy, fmt.Sprintf("MCPServerGroup %q not found", name)), nil
			}
			return metav1.LabelSelector{}, "", nil, err
		}
		members, err := r.groupMemberNames(ctx, group)
		if err != nil {
			return metav1.LabelSelector{}, "", nil, err
		}
		if len(members) == 0 {
			return metav1.LabelSelector{}, "", r.targetNotFound(policy,
				fmt.Sprintf("MCPServerGroup %q has no member servers", name)), nil
		}
		sel := metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      networkpolicy.LabelProvider,
			Operator: metav1.LabelSelectorOpIn,
			Values:   members,
		}}}
		return sel, name, nil, nil

	default:
		// The CRD enum restricts kind to MCPServer|MCPServerGroup; this guards
		// an object that slipped past validation.
		r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionFalse,
			"UnsupportedTargetKind", fmt.Sprintf("targetRef.kind %q is not supported", policy.Spec.TargetRef.Kind))
		r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionFalse,
			"UnsupportedTargetKind", "no backstop applied")
		r.clearDegraded(policy)
		return metav1.LabelSelector{}, "", &ctrl.Result{}, nil
	}
}

// groupMemberNames returns the sorted names of the MCPServers matching a
// group's selector in the group's namespace.
func (r *MCPEgressPolicyReconciler) groupMemberNames(ctx context.Context, group *mcpv1alpha2.MCPServerGroup) ([]string, error) {
	if group.Spec.Selector == nil {
		return nil, nil
	}
	sel, err := metav1.LabelSelectorAsSelector(group.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("group %q selector: %w", group.Name, err)
	}
	var list mcpv1alpha2.MCPServerList
	if err := r.List(ctx, &list,
		client.InNamespace(group.Namespace),
		client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	sort.Strings(names)
	return names, nil
}

// targetNotFound sets the withheld-backstop conditions and returns a requeue
// result for a policy whose target does not (yet) exist or has no members.
func (r *MCPEgressPolicyReconciler) targetNotFound(policy *mcpv1alpha2.MCPEgressPolicy, msg string) *ctrl.Result {
	r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionFalse, "TargetNotFound", msg)
	r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionFalse, "TargetNotFound", "no backstop applied")
	r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue, "TargetNotFound", msg)
	return &ctrl.Result{RequeueAfter: targetNotFoundRequeueAfter * 1e9}
}

// applyFlavoredBackstop resolves the effective backstop flavor and applies it,
// cleaning up the other flavor's object, then records status conditions.
//
// Flavor resolution: Vanilla and Cilium are honored explicitly; Auto (and an
// omitted networkBackstop) uses Cilium when its CRD is installed, else Vanilla.
// Cilium requested on a cluster without the CRD falls back to the Vanilla floor
// and reports Degraded, rather than failing open.
func (r *MCPEgressPolicyReconciler) applyFlavoredBackstop(ctx context.Context, logger logr.Logger, policy *mcpv1alpha2.MCPEgressPolicy, selector metav1.LabelSelector, targetName string) error {
	requested := mcpv1alpha2.BackstopFlavorAuto
	if policy.Spec.NetworkBackstop != nil && policy.Spec.NetworkBackstop.Flavor != "" {
		requested = policy.Spec.NetworkBackstop.Flavor
	}
	ciliumAvailable := r.ciliumAvailable()
	useCilium := (requested == mcpv1alpha2.BackstopFlavorCilium || requested == mcpv1alpha2.BackstopFlavorAuto) && ciliumAvailable

	r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionTrue, "Compiled", "Policy compiled")

	if useCilium {
		cnp := networkpolicy.BuildEgressPolicyCiliumNetworkPolicy(policy, selector)
		if err := controllerutil.SetControllerReference(policy, cnp, r.Scheme); err != nil {
			return fmt.Errorf("set owner reference on cilium backstop: %w", err)
		}
		if err := r.applyCiliumBackstop(ctx, policy, cnp); err != nil {
			return err
		}
		if err := r.deleteBackstopIfExists(ctx, policy); err != nil { // remove the Vanilla NP if switching
			return err
		}
		r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionTrue,
			"BackstopApplied", fmt.Sprintf("Cilium backstop %q applied for %q (FQDN + CIDR enforced)", cnp.GetName(), targetName))
		r.clearDegraded(policy)
		logger.Info("Reconciled MCPEgressPolicy backstop", "policy", policy.Name, "target", targetName, "flavor", "Cilium")
		return nil
	}

	// Vanilla path.
	desired, unenforceable := networkpolicy.BuildEgressPolicyBackstop(policy, selector)
	if err := controllerutil.SetControllerReference(policy, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on backstop: %w", err)
	}
	if err := r.applyBackstop(ctx, policy, desired); err != nil {
		return err
	}
	if err := r.deleteCiliumBackstopIfExists(ctx, policy, ciliumAvailable); err != nil { // remove the CNP if switching
		return err
	}
	r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionTrue,
		"BackstopApplied", fmt.Sprintf("Vanilla backstop %q applied", desired.Name))

	// A Vanilla NetworkPolicy cannot match FQDNs: hostname upstreams are denied
	// (fail closed), not opened. Surface the gap.
	switch {
	case requested == mcpv1alpha2.BackstopFlavorCilium && !ciliumAvailable:
		r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue, "CiliumUnavailable",
			"spec.networkBackstop.flavor=Cilium but the CiliumNetworkPolicy CRD is not installed; applied the Vanilla floor (FQDN upstreams not enforced)")
	case len(unenforceable) > 0:
		r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue, "FQDNUpstreamsUnenforceable",
			fmt.Sprintf("FQDN upstreams denied under the Vanilla backstop (need the Cilium flavor): %s",
				strings.Join(unenforceable, ", ")))
	default:
		r.clearDegraded(policy)
	}
	logger.Info("Reconciled MCPEgressPolicy backstop",
		"policy", policy.Name, "flavor", "Vanilla", "unenforceableUpstreams", len(unenforceable))
	return nil
}

// applyBackstop creates or updates the desired backstop NetworkPolicy.
func (r *MCPEgressPolicyReconciler) applyBackstop(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy, desired *networkingv1.NetworkPolicy) error {
	existing := &networkingv1.NetworkPolicy{}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create backstop: %w", err)
		}
		r.Recorder.Event(policy, corev1.EventTypeNormal, "BackstopCreated",
			fmt.Sprintf("Created network backstop %s", desired.Name))
		return nil
	}
	if err != nil {
		return err
	}
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update backstop: %w", err)
		}
		r.Recorder.Event(policy, corev1.EventTypeNormal, "BackstopUpdated",
			fmt.Sprintf("Updated network backstop %s", desired.Name))
	}
	return nil
}

// deleteBackstopIfExists removes the backstop NetworkPolicy for a policy if present.
func (r *MCPEgressPolicyReconciler) deleteBackstopIfExists(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy) error {
	np := &networkingv1.NetworkPolicy{}
	key := types.NamespacedName{Name: networkpolicy.EgressPolicyBackstopName(policy.Name), Namespace: policy.Namespace}
	if err := r.Get(ctx, key, np); err != nil {
		return client.IgnoreNotFound(err)
	}
	if err := r.Delete(ctx, np); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// ciliumAvailable reports whether the CiliumNetworkPolicy CRD is installed, so
// the operator can pick the Cilium flavor on Auto and enforce FQDN upstreams.
func (r *MCPEgressPolicyReconciler) ciliumAvailable() bool {
	_, err := r.RESTMapper().RESTMapping(
		schema.GroupKind{Group: networkpolicy.CiliumGroup, Kind: networkpolicy.CiliumNetworkPolicyKind},
		networkpolicy.CiliumVersion)
	return err == nil
}

// newCiliumBackstopStub returns an empty unstructured CiliumNetworkPolicy with
// its GVK and key set, for Get/Delete.
func newCiliumBackstopStub(policy *mcpv1alpha2.MCPEgressPolicy) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: networkpolicy.CiliumGroup, Version: networkpolicy.CiliumVersion, Kind: networkpolicy.CiliumNetworkPolicyKind,
	})
	u.SetName(networkpolicy.EgressPolicyBackstopName(policy.Name))
	u.SetNamespace(policy.Namespace)
	return u
}

// applyCiliumBackstop creates or updates the desired CiliumNetworkPolicy.
func (r *MCPEgressPolicyReconciler) applyCiliumBackstop(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy, desired *unstructured.Unstructured) error {
	existing := newCiliumBackstopStub(policy)
	err := r.Get(ctx, types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}, existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create cilium backstop: %w", err)
		}
		r.Recorder.Event(policy, corev1.EventTypeNormal, "BackstopCreated",
			fmt.Sprintf("Created Cilium network backstop %s", desired.GetName()))
		return nil
	}
	if err != nil {
		return err
	}
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	if !equality.Semantic.DeepEqual(existingSpec, desiredSpec) {
		_ = unstructured.SetNestedMap(existing.Object, desiredSpec, "spec")
		existing.SetLabels(desired.GetLabels())
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update cilium backstop: %w", err)
		}
		r.Recorder.Event(policy, corev1.EventTypeNormal, "BackstopUpdated",
			fmt.Sprintf("Updated Cilium network backstop %s", desired.GetName()))
	}
	return nil
}

// deleteCiliumBackstopIfExists removes the CiliumNetworkPolicy backstop if
// present. It is a no-op when the Cilium CRD is absent (nothing could exist,
// and the API call would fail with no-kind-match).
func (r *MCPEgressPolicyReconciler) deleteCiliumBackstopIfExists(ctx context.Context, policy *mcpv1alpha2.MCPEgressPolicy, ciliumAvailable bool) error {
	if !ciliumAvailable {
		return nil
	}
	cnp := newCiliumBackstopStub(policy)
	if err := r.Get(ctx, types.NamespacedName{Name: cnp.GetName(), Namespace: cnp.GetNamespace()}, cnp); err != nil {
		return client.IgnoreNotFound(err)
	}
	if err := r.Delete(ctx, cnp); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

func (r *MCPEgressPolicyReconciler) setCondition(policy *mcpv1alpha2.MCPEgressPolicy, condType string, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: policy.Generation,
	})
}

// clearDegraded sets Degraded=False (no active degradation).
func (r *MCPEgressPolicyReconciler) clearDegraded(policy *mcpv1alpha2.MCPEgressPolicy) {
	r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionFalse, "NotDegraded", "No degradation")
}

// SetupWithManager wires the controller.
func (r *MCPEgressPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha2.MCPEgressPolicy{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("mcpegresspolicy").
		Complete(r)
}

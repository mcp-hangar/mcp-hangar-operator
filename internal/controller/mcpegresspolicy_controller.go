package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
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

// MCPEgressPolicyReconciler reconciles an MCPEgressPolicy into its L3/L4 network
// backstop. This slice implements the Vanilla backstop (default-deny egress +
// DNS + CIDR upstreams). FQDN upstream enforcement (Cilium toFQDNs) and the
// L7 data-plane document land in follow-ups (epic #53, per ADR-013).
type MCPEgressPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpegresspolicies/finalizers,verbs=update

// Reconcile ensures the network backstop for a policy matches its spec, then
// records the outcome in status.
func (r *MCPEgressPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &mcpv1alpha2.MCPEgressPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
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

	// Only MCPServer targets get a backstop in this slice; MCPServerGroup
	// (a selector over many servers) is a follow-up.
	if policy.Spec.TargetRef.Kind != "MCPServer" {
		if err := r.deleteBackstopIfExists(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionFalse,
			"UnsupportedTargetKind",
			fmt.Sprintf("targetRef.kind %q is not yet supported by the backstop (only MCPServer)", policy.Spec.TargetRef.Kind))
		r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionFalse,
			"UnsupportedTargetKind", "no backstop applied")
		r.clearDegraded(policy)
		return ctrl.Result{}, nil
	}

	// Resolve the target server -- its pods carry LabelProvider=<name>.
	target := &mcpv1alpha2.MCPServer{}
	targetKey := types.NamespacedName{Name: policy.Spec.TargetRef.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, targetKey, target); err != nil {
		if apierrors.IsNotFound(err) {
			r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionFalse,
				"TargetNotFound", fmt.Sprintf("MCPServer %q not found", policy.Spec.TargetRef.Name))
			r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionFalse,
				"TargetNotFound", "no backstop applied")
			r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue,
				"TargetNotFound", "policy target does not exist; backstop withheld")
			return ctrl.Result{RequeueAfter: targetNotFoundRequeueAfter * 1e9}, nil
		}
		return ctrl.Result{}, err
	}

	selector := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: target.Name}}
	desired, unenforceable := networkpolicy.BuildEgressPolicyBackstop(policy, selector)
	if err := controllerutil.SetControllerReference(policy, desired, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("set owner reference on backstop: %w", err)
	}

	if err := r.applyBackstop(ctx, policy, desired); err != nil {
		return ctrl.Result{}, err
	}

	r.setCondition(policy, EgressPolicyConditionCompiled, metav1.ConditionTrue, "Compiled", "Policy compiled")
	r.setCondition(policy, EgressPolicyConditionBackstopApplied, metav1.ConditionTrue,
		"BackstopApplied", fmt.Sprintf("Vanilla backstop %q applied", desired.Name))

	// FQDN upstreams cannot be enforced by a Vanilla NetworkPolicy: they are
	// denied (fail closed), so the policy does not open them. Surface the gap --
	// enforcing them needs the Cilium flavor (follow-up).
	wantsCilium := policy.Spec.NetworkBackstop != nil && policy.Spec.NetworkBackstop.Flavor == mcpv1alpha2.BackstopFlavorCilium
	switch {
	case len(unenforceable) > 0:
		r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue, "FQDNUpstreamsUnenforceable",
			fmt.Sprintf("FQDN upstreams denied under the Vanilla backstop (need the Cilium flavor): %s",
				strings.Join(unenforceable, ", ")))
	case wantsCilium:
		r.setCondition(policy, EgressPolicyConditionDegraded, metav1.ConditionTrue, "CiliumFlavorNotImplemented",
			"spec.networkBackstop.flavor=Cilium requested; the Vanilla backstop was applied (Cilium is a follow-up)")
	default:
		r.clearDegraded(policy)
	}

	logger.Info("Reconciled MCPEgressPolicy backstop",
		"policy", policy.Name, "target", target.Name,
		"backstop", desired.Name, "unenforceableUpstreams", len(unenforceable))
	return ctrl.Result{}, nil
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

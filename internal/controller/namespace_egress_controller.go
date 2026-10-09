package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// ReasonDefaultDenyNotOwned is the Warning Event reason emitted on a Namespace
// when a NetworkPolicy named like the default-deny backstop exists but is not
// the operator's: owned by another controller, or carrying neither an owner
// nor the operator's managed-by label. The operator does not adopt it (#204).
const ReasonDefaultDenyNotOwned = "DefaultDenyNotOwned"

// foreignDefaultDenyRequeue bounds how long an opted-in namespace stays
// without the operator's backstop after a foreign policy under the backstop's
// name goes away. A foreign policy carries no owner reference to the
// Namespace, so its deletion never reaches the Owns() watch (#204).
const foreignDefaultDenyRequeue = 5 * time.Minute

// NamespaceEgressReconciler maintains the namespace-wide default-deny egress
// NetworkPolicy for namespaces that opt into enforcement via the
// mcp-hangar.io/enforce-egress=true label (#51). In an opted-in namespace, pods
// not covered by a per-server allow policy are limited to DNS -- shadow /
// unregistered workloads get no egress to upstreams.
type NamespaceEgressReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// Reconcile ensures the default-deny egress policy exists in opted-in namespaces
// and is absent otherwise.
func (r *NamespaceEgressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ns corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ns.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	optedIn := ns.Labels[networkpolicy.EnforceEgressLabel] == "true"
	key := types.NamespacedName{Namespace: ns.Name, Name: networkpolicy.DefaultDenyEgressName}

	var existing networkingv1.NetworkPolicy
	getErr := r.Get(ctx, key, &existing)

	if !optedIn {
		// Not opted in: remove our default-deny policy if we created one. A
		// same-named policy someone else controls is not ours to delete (#236),
		// the same rule the opted-in path applies before adopting.
		if getErr == nil && defaultDenyOwnedByOperator(&ns, &existing) {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("delete default-deny egress: %w", err)
			}
			logger.Info("removed default-deny egress (namespace not opted in)", "namespace", ns.Name)
		}
		return ctrl.Result{}, nil
	}

	desired := networkpolicy.BuildNamespaceDefaultDenyEgress(ns.Name)
	// The Namespace controls the policy (#204): the Owns() watch in
	// SetupWithManager re-runs this reconcile when the policy is deleted or
	// edited, and garbage collection removes it with the namespace. A
	// cluster-scoped owner of a namespaced dependent is valid.
	if err := controllerutil.SetControllerReference(&ns, desired, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("set owner reference on default-deny egress: %w", err)
	}

	if apierrors.IsNotFound(getErr) {
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("create default-deny egress: %w", err)
		}
		logger.Info("created default-deny egress", "namespace", ns.Name)
		return ctrl.Result{}, nil
	}
	if getErr != nil {
		return ctrl.Result{}, fmt.Errorf("get default-deny egress: %w", getErr)
	}

	// A same-named policy that is not ours is left alone rather than
	// overwritten: adopting it would silently take over whatever someone else
	// put there. Say so on the Namespace and come back later (#204).
	if !defaultDenyOwnedByOperator(&ns, &existing) {
		logger.Info("default-deny egress exists but is not managed by the operator; not adopting",
			"namespace", ns.Name, "networkPolicy", existing.Name)
		r.Recorder.Eventf(&ns, nil, corev1.EventTypeWarning, ReasonDefaultDenyNotOwned, ActionReconcile,
			"NetworkPolicy %s/%s exists but is not managed by the operator; the operator's default-deny egress backstop is not applied until it is removed",
			existing.Namespace, existing.Name)
		return ctrl.Result{RequeueAfter: foreignDefaultDenyRequeue}, nil
	}

	// Idempotent: reconcile drift back to the desired spec, labels and owner.
	// The owner is also set on policies written before #204, which carried
	// none, so an upgrade brings them under the watch.
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) ||
		!labelsContain(existing.Labels, desired.Labels) ||
		!metav1.IsControlledBy(&existing, &ns) {
		existing.Spec = desired.Spec
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			existing.Labels[k] = v
		}
		if err := controllerutil.SetControllerReference(&ns, &existing, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set owner reference on default-deny egress: %w", err)
		}
		if err := r.Update(ctx, &existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("update default-deny egress: %w", err)
		}
		logger.Info("reconciled default-deny egress spec", "namespace", ns.Name)
	}

	return ctrl.Result{}, nil
}

// defaultDenyOwnedByOperator reports whether np is the operator's backstop for
// ns: controlled by the Namespace, or -- for a policy written before #204 --
// controlled by nothing and carrying the operator's managed-by label.
func defaultDenyOwnedByOperator(ns *corev1.Namespace, np *networkingv1.NetworkPolicy) bool {
	if metav1.IsControlledBy(np, ns) {
		return true
	}
	return metav1.GetControllerOf(np) == nil &&
		np.Labels[networkpolicy.LabelManagedBy] == networkpolicy.DefaultManagerName
}

// labelsContain reports whether every label in want is present in have with
// the same value.
func labelsContain(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// SetupWithManager registers the reconciler for Namespace events and for
// events on the default-deny policies the Namespace controls. The MCPServer
// and MCPEgressPolicy controllers also own NetworkPolicies; Owns() routes by
// the controller owner's kind, so their policies never enqueue a Namespace.
func (r *NamespaceEgressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}

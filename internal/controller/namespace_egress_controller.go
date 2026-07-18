package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

// NamespaceEgressReconciler maintains the namespace-wide default-deny egress
// NetworkPolicy for namespaces that opt into enforcement via the
// mcp-hangar.io/enforce-egress=true label (#51). In an opted-in namespace, pods
// not covered by a per-server allow policy are limited to DNS -- shadow /
// unregistered workloads get no egress to upstreams.
type NamespaceEgressReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
		// Not opted in: remove our default-deny policy if we created one.
		if getErr == nil {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("delete default-deny egress: %w", err)
			}
			logger.Info("removed default-deny egress (namespace not opted in)", "namespace", ns.Name)
		}
		return ctrl.Result{}, nil
	}

	desired := networkpolicy.BuildNamespaceDefaultDenyEgress(ns.Name)

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

	// Idempotent: reconcile drift back to the desired spec.
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		if err := r.Update(ctx, &existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("update default-deny egress: %w", err)
		}
		logger.Info("reconciled default-deny egress spec", "namespace", ns.Name)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler for Namespace events.
func (r *NamespaceEgressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Complete(r)
}

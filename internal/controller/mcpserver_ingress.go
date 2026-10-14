package controller

import (
	"context"
	"fmt"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ConditionProviderIngressApplied = "ProviderIngressApplied"

func (r *MCPServerReconciler) reconcileProviderIngress(ctx context.Context, server *mcpv1alpha2.MCPServer) error {
	enabled := r.ProviderIngress.Enabled
	reason := "Disabled"
	if value, ok := server.Annotations[networkpolicy.AnnotationProviderIngress]; ok && !server.IsRemoteMode() {
		switch value {
		case "true":
			enabled = true
		case "false":
			enabled = false
		default:
			return fmt.Errorf("%s must be true or false", networkpolicy.AnnotationProviderIngress)
		}
	}
	if server.IsRemoteMode() {
		enabled = false
		reason = "NotApplicable"
	}
	key := types.NamespacedName{Name: networkpolicy.ProviderIngressPolicyName(server.Name), Namespace: server.Namespace}
	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, key, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	exists := err == nil
	if exists && !metav1.IsControlledBy(existing, server) {
		if enabled {
			return fmt.Errorf("ingress NetworkPolicy %s exists with a different owner", key)
		}
		exists = false // opt-out must neither delete nor block an unrelated policy
	}
	if !enabled {
		if exists {
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		setServerCondition(server, ConditionProviderIngressApplied, metav1.ConditionFalse, reason, "no provider ingress fence requested or applicable")
		return nil
	}
	desired, err := networkpolicy.BuildProviderIngress(server, r.ProviderIngress)
	if err != nil {
		return err
	}
	if err = controllerutil.SetControllerReference(server, desired, r.Scheme); err != nil {
		return err
	}
	if !exists || !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) || !labelsContain(existing.Labels, desired.Labels) {
		if err = applyOwned(ctx, r.Client, desired); err != nil {
			return fmt.Errorf("apply provider ingress policy: %w", err)
		}
	}
	signal := r.EnforcementProbe.Signal(ctx)
	status, reason := metav1.ConditionUnknown, "EnforcementUnverified"
	switch signal.Verdict {
	case networkpolicy.EnforcementObserved:
		status, reason = metav1.ConditionTrue, "PolicyApplied"
	case networkpolicy.EnforcementUnknown:
		// Unknown is deliberately not reported as successful enforcement.
	case networkpolicy.EnforcementNotObserved:
		status, reason = metav1.ConditionFalse, "EnforcementNotObserved"
	}
	setServerCondition(server, ConditionProviderIngressApplied, status, reason,
		fmt.Sprintf("ingress TCP/8080 from namespace %q pods %s; %s; independent allow policies may widen ingress", r.ProviderIngress.GatewayNamespace, metav1.FormatLabelSelector(r.ProviderIngress.GatewaySelector), signal.Source))
	return nil
}

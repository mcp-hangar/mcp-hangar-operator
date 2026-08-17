package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// validateGroupSelector is the whole of MCPServerGroup admission.
//
// It used to be a `groupConstraints` struct shared by both versions, which
// earned its keep while there were session-affinity and duration rules to run.
// Those validated fields the operator never honoured (#123): a well-formed
// no-op passed admission and was stored, which reads as configuration. With
// them gone the shared rule set is one predicate, and a struct to carry one
// bool between two callers is further than the thing it abstracts.
//
// A group with no selector can never match members. The schema marks it
// required; this validates defensively, and is the only reason the group
// webhook still exists.
func validateGroupSelector(hasSelector bool) error {
	if !hasSelector {
		return fmt.Errorf("MCPServerGroup validation failed: spec.selector is required")
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpservergroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservergroups,verbs=create;update,versions=v1alpha2,name=vmcpservergroup-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPServerGroupV1alpha2Validator validates v1alpha2 (storage) MCPServerGroup
// resources.
type MCPServerGroupV1alpha2Validator struct{}

var _ admission.CustomValidator = &MCPServerGroupV1alpha2Validator{}

// ValidateCreate validates a v1alpha2 MCPServerGroup on creation.
func (v *MCPServerGroupV1alpha2Validator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	g, ok := obj.(*mcpv1alpha2.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPServerGroup, got %T", obj)
	}
	return nil, validateGroupSelector(g.Spec.Selector != nil)
}

// ValidateUpdate validates a v1alpha2 MCPServerGroup on update.
func (v *MCPServerGroupV1alpha2Validator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	g, ok := newObj.(*mcpv1alpha2.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPServerGroup, got %T", newObj)
	}
	return nil, validateGroupSelector(g.Spec.Selector != nil)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerGroupV1alpha2Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

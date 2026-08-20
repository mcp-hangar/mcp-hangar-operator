package webhook

import (
	"context"
	"fmt"

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

var _ admission.Validator[*mcpv1alpha2.MCPServerGroup] = &MCPServerGroupV1alpha2Validator{}

// ValidateCreate validates a v1alpha2 MCPServerGroup on creation.
func (v *MCPServerGroupV1alpha2Validator) ValidateCreate(_ context.Context, obj *mcpv1alpha2.MCPServerGroup) (admission.Warnings, error) {
	if obj == nil {
		return nil, fmt.Errorf("MCPServerGroup object is nil")
	}
	return nil, validateGroupSelector(obj.Spec.Selector != nil)
}

// ValidateUpdate validates a v1alpha2 MCPServerGroup on update.
func (v *MCPServerGroupV1alpha2Validator) ValidateUpdate(_ context.Context, _, newObj *mcpv1alpha2.MCPServerGroup) (admission.Warnings, error) {
	if newObj == nil {
		return nil, fmt.Errorf("MCPServerGroup object is nil")
	}
	return nil, validateGroupSelector(newObj.Spec.Selector != nil)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerGroupV1alpha2Validator) ValidateDelete(_ context.Context, _ *mcpv1alpha2.MCPServerGroup) (admission.Warnings, error) {
	return nil, nil
}

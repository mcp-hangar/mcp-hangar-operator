package webhook

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// groupConstraints holds the version-agnostic subset of an MCPServerGroup spec
// that admission validation inspects. Extracting into a common struct lets the
// v1alpha1 and v1alpha2 validators share a single rule implementation.
type groupConstraints struct {
	hasSelector           bool
	hasSessionAffinity    bool
	sessionAffinityType   string
	sessionAffinityHeader string
	// durations holds free-form duration strings (field path -> raw value).
	// Populated only for v1alpha1, whose duration fields are plain strings;
	// v1alpha2 models them as *metav1.Duration and leaves this nil.
	durations map[string]string
}

// validateGroupConstraints runs the shared MCPServerGroup validation rules.
func validateGroupConstraints(c groupConstraints) error {
	var errs []string

	// A group with no selector can never match members. The schema marks
	// selector required, but validate defensively.
	if !c.hasSelector {
		errs = append(errs, "spec.selector is required")
	}

	// Header-based session affinity needs a header name to key on.
	if c.hasSessionAffinity && c.sessionAffinityType == "Header" && c.sessionAffinityHeader == "" {
		errs = append(errs, "spec.sessionAffinity.header is required when spec.sessionAffinity.type is \"Header\"")
	}

	// Duration strings must parse, else conversion to v1alpha2 hard-fails and
	// the object becomes unconvertible after admission accepted it.
	errs = append(errs, validateDurationStrings(c.durations)...)

	if len(errs) > 0 {
		return fmt.Errorf("MCPServerGroup validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha1-mcpservergroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservergroups,verbs=create;update,versions=v1alpha1,name=vmcpservergroup-v1alpha1.kb.io,admissionReviewVersions=v1

// MCPServerGroupValidator validates v1alpha1 MCPServerGroup resources.
type MCPServerGroupValidator struct{}

var _ admission.CustomValidator = &MCPServerGroupValidator{}

func groupConstraintsFromV1alpha1(g *mcpv1alpha1.MCPServerGroup) groupConstraints {
	c := groupConstraints{
		hasSelector: g.Spec.Selector != nil,
		durations:   map[string]string{},
	}
	if g.Spec.SessionAffinity != nil {
		c.hasSessionAffinity = true
		c.sessionAffinityType = g.Spec.SessionAffinity.Type
		c.sessionAffinityHeader = g.Spec.SessionAffinity.Header
		c.durations["spec.sessionAffinity.ttl"] = g.Spec.SessionAffinity.TTL
	}
	if g.Spec.Failover != nil {
		c.durations["spec.failover.retryDelay"] = g.Spec.Failover.RetryDelay
	}
	if g.Spec.CircuitBreaker != nil {
		c.durations["spec.circuitBreaker.resetTimeout"] = g.Spec.CircuitBreaker.ResetTimeout
	}
	return c
}

// ValidateCreate validates a v1alpha1 MCPServerGroup on creation.
func (v *MCPServerGroupValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	g, ok := obj.(*mcpv1alpha1.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha1 MCPServerGroup, got %T", obj)
	}
	return nil, validateGroupConstraints(groupConstraintsFromV1alpha1(g))
}

// ValidateUpdate validates a v1alpha1 MCPServerGroup on update.
func (v *MCPServerGroupValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	g, ok := newObj.(*mcpv1alpha1.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha1 MCPServerGroup, got %T", newObj)
	}
	return nil, validateGroupConstraints(groupConstraintsFromV1alpha1(g))
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerGroupValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpservergroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservergroups,verbs=create;update,versions=v1alpha2,name=vmcpservergroup-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPServerGroupV1alpha2Validator validates v1alpha2 (storage) MCPServerGroup
// resources.
type MCPServerGroupV1alpha2Validator struct{}

var _ admission.CustomValidator = &MCPServerGroupV1alpha2Validator{}

func groupConstraintsFromV1alpha2(g *mcpv1alpha2.MCPServerGroup) groupConstraints {
	c := groupConstraints{hasSelector: g.Spec.Selector != nil}
	if g.Spec.SessionAffinity != nil {
		c.hasSessionAffinity = true
		c.sessionAffinityType = g.Spec.SessionAffinity.Type
		c.sessionAffinityHeader = g.Spec.SessionAffinity.Header
	}
	return c
}

// ValidateCreate validates a v1alpha2 MCPServerGroup on creation.
func (v *MCPServerGroupV1alpha2Validator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	g, ok := obj.(*mcpv1alpha2.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPServerGroup, got %T", obj)
	}
	return nil, validateGroupConstraints(groupConstraintsFromV1alpha2(g))
}

// ValidateUpdate validates a v1alpha2 MCPServerGroup on update.
func (v *MCPServerGroupV1alpha2Validator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	g, ok := newObj.(*mcpv1alpha2.MCPServerGroup)
	if !ok || g == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPServerGroup, got %T", newObj)
	}
	return nil, validateGroupConstraints(groupConstraintsFromV1alpha2(g))
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerGroupV1alpha2Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

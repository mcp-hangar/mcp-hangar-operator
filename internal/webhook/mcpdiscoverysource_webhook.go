package webhook

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// discoveryConstraints holds the version-agnostic subset of an
// MCPDiscoverySource spec that admission validation inspects.
type discoveryConstraints struct {
	discoveryType   string
	hasConfigMapRef bool
	includePatterns []string
	excludePatterns []string
}

// validateDiscoveryConstraints runs the shared MCPDiscoverySource rules.
func validateDiscoveryConstraints(c discoveryConstraints) error {
	var errs []string

	// ConfigMap-type sources must reference a ConfigMap.
	if c.discoveryType == "ConfigMap" && !c.hasConfigMapRef {
		errs = append(errs, "spec.configMapRef is required when spec.type is \"ConfigMap\"")
	}

	// Filter patterns are regular expressions; reject ones that do not compile,
	// otherwise the controller would fail every reconcile at runtime.
	for i, p := range c.includePatterns {
		if _, err := regexp.Compile(p); err != nil {
			errs = append(errs, fmt.Sprintf("spec.filters.includePatterns[%d] %q is not a valid regexp: %v", i, p, err))
		}
	}
	for i, p := range c.excludePatterns {
		if _, err := regexp.Compile(p); err != nil {
			errs = append(errs, fmt.Sprintf("spec.filters.excludePatterns[%d] %q is not a valid regexp: %v", i, p, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("MCPDiscoverySource validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha1-mcpdiscoverysource,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpdiscoverysources,verbs=create;update,versions=v1alpha1,name=vmcpdiscoverysource-v1alpha1.kb.io,admissionReviewVersions=v1

// MCPDiscoverySourceValidator validates v1alpha1 MCPDiscoverySource resources.
type MCPDiscoverySourceValidator struct{}

var _ admission.CustomValidator = &MCPDiscoverySourceValidator{}

func discoveryConstraintsFromV1alpha1(d *mcpv1alpha1.MCPDiscoverySource) discoveryConstraints {
	c := discoveryConstraints{
		discoveryType:   string(d.Spec.Type),
		hasConfigMapRef: d.Spec.ConfigMapRef != nil,
	}
	if d.Spec.Filters != nil {
		c.includePatterns = d.Spec.Filters.IncludePatterns
		c.excludePatterns = d.Spec.Filters.ExcludePatterns
	}
	return c
}

// ValidateCreate validates a v1alpha1 MCPDiscoverySource on creation.
func (v *MCPDiscoverySourceValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	d, ok := obj.(*mcpv1alpha1.MCPDiscoverySource)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha1 MCPDiscoverySource, got %T", obj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha1(d))
}

// ValidateUpdate validates a v1alpha1 MCPDiscoverySource on update.
func (v *MCPDiscoverySourceValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	d, ok := newObj.(*mcpv1alpha1.MCPDiscoverySource)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha1 MCPDiscoverySource, got %T", newObj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha1(d))
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPDiscoverySourceValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpdiscoverysource,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpdiscoverysources,verbs=create;update,versions=v1alpha2,name=vmcpdiscoverysource-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPDiscoverySourceV1alpha2Validator validates v1alpha2 (storage)
// MCPDiscoverySource resources.
type MCPDiscoverySourceV1alpha2Validator struct{}

var _ admission.CustomValidator = &MCPDiscoverySourceV1alpha2Validator{}

func discoveryConstraintsFromV1alpha2(d *mcpv1alpha2.MCPDiscoverySource) discoveryConstraints {
	c := discoveryConstraints{
		discoveryType:   string(d.Spec.Type),
		hasConfigMapRef: d.Spec.ConfigMapRef != nil,
	}
	if d.Spec.Filters != nil {
		c.includePatterns = d.Spec.Filters.IncludePatterns
		c.excludePatterns = d.Spec.Filters.ExcludePatterns
	}
	return c
}

// ValidateCreate validates a v1alpha2 MCPDiscoverySource on creation.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	d, ok := obj.(*mcpv1alpha2.MCPDiscoverySource)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha2 MCPDiscoverySource, got %T", obj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha2(d))
}

// ValidateUpdate validates a v1alpha2 MCPDiscoverySource on update.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	d, ok := newObj.(*mcpv1alpha2.MCPDiscoverySource)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha2 MCPDiscoverySource, got %T", newObj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha2(d))
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

package webhook

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// discoveryConstraints holds the version-agnostic subset of an
// MCPDiscoverySource spec that admission validation inspects.
type discoveryConstraints struct {
	discoveryType   string
	hasConfigMapRef bool
	includePatterns []string
	excludePatterns []string
	// durations holds free-form duration strings (field path -> raw value).
	// Populated only for v1alpha1, whose duration fields are plain strings;
	// v1alpha2 models them as *metav1.Duration and leaves this nil.
	durations map[string]string
}

// validateDiscoveryConstraints runs the shared MCPDiscoverySource rules.
func validateDiscoveryConstraints(c discoveryConstraints) error {
	var errs []string

	// ConfigMap-type sources must reference a ConfigMap.
	if c.discoveryType == "ConfigMap" && !c.hasConfigMapRef {
		errs = append(errs, "spec.configMapRef is required when spec.type is \"ConfigMap\"")
	}

	// Duration strings must parse, else conversion to v1alpha2 hard-fails and
	// the object becomes unconvertible after admission accepted it.
	errs = append(errs, validateDurationStrings(c.durations)...)

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
	if !ok || d == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPDiscoverySource, got %T", obj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha2(d))
}

// ValidateUpdate validates a v1alpha2 MCPDiscoverySource on update.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	d, ok := newObj.(*mcpv1alpha2.MCPDiscoverySource)
	if !ok || d == nil {
		return nil, fmt.Errorf("expected v1alpha2 MCPDiscoverySource, got %T", newObj)
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha2(d))
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateDurationStrings parses each non-empty duration value and returns an
// error message for any that is unparseable or negative. It is shared by the
// v1alpha1 validators, whose duration fields are free-form strings; conversion
// to v1alpha2 hard-fails on a bad value, so rejecting it at admission keeps the
// stored object convertible. (v1alpha2 models durations as *metav1.Duration,
// which the apiserver already validates structurally.)
func validateDurationStrings(fields map[string]string) []string {
	var errs []string
	for field, val := range fields {
		if val == "" {
			continue
		}
		d, err := time.ParseDuration(val)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s %q is not a valid duration: %v", field, val, err))
		} else if d < 0 {
			errs = append(errs, fmt.Sprintf("%s must not be negative", field))
		}
	}
	return errs
}

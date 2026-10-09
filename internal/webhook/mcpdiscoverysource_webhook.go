package webhook

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// discoveryConstraints holds the version-agnostic subset of an
// MCPDiscoverySource spec that admission validation inspects.
type discoveryConstraints struct {
	discoveryType   string
	hasConfigMapRef bool
	// namespace is the source's own namespace; configMapNamespace is
	// spec.configMapRef.namespace (empty means the source's namespace).
	namespace          string
	configMapNamespace string
	includePatterns    []string
	excludePatterns    []string
	// durations holds free-form duration strings (field path -> raw value).
	// Populated only for v1alpha1, whose duration fields are plain strings;
	// v1alpha2 models them as *metav1.Duration and leaves this nil.
	durations map[string]string
}

// validateDiscoveryConstraints runs the shared MCPDiscoverySource rules.
func validateDiscoveryConstraints(c discoveryConstraints) error {
	var errs []string

	// A ConfigMap-type source without a configMapRef is a CRD CEL rule (#196):
	// the apiserver refuses it before this webhook is called.

	// A ConfigMap source may only read its own namespace (#234). The
	// controller refuses a cross-namespace reference too, with the webhook
	// off; this gives the clear admission error.
	if c.crossNamespace() {
		errs = append(errs, crossNamespaceMessage(c))
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

// crossNamespace reports whether a ConfigMap source references a ConfigMap
// outside its own namespace.
func (c discoveryConstraints) crossNamespace() bool {
	return c.discoveryType == "ConfigMap" && c.hasConfigMapRef &&
		c.configMapNamespace != "" && c.configMapNamespace != c.namespace
}

func crossNamespaceMessage(c discoveryConstraints) string {
	return fmt.Sprintf("spec.configMapRef.namespace %q must be empty or equal to the source namespace %q: "+
		"a ConfigMap source may only read its own namespace", c.configMapNamespace, c.namespace)
}

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpdiscoverysource,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpdiscoverysources,verbs=create;update,versions=v1alpha2,name=vmcpdiscoverysource-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPDiscoverySourceV1alpha2Validator validates v1alpha2 (storage)
// MCPDiscoverySource resources.
type MCPDiscoverySourceV1alpha2Validator struct{}

var _ admission.Validator[*mcpv1alpha2.MCPDiscoverySource] = &MCPDiscoverySourceV1alpha2Validator{}

func discoveryConstraintsFromV1alpha2(d *mcpv1alpha2.MCPDiscoverySource) discoveryConstraints {
	c := discoveryConstraints{
		discoveryType:   string(d.Spec.Type),
		hasConfigMapRef: d.Spec.ConfigMapRef != nil,
		namespace:       d.Namespace,
	}
	if d.Spec.ConfigMapRef != nil {
		c.configMapNamespace = d.Spec.ConfigMapRef.Namespace
	}
	if d.Spec.Filters != nil {
		c.includePatterns = d.Spec.Filters.IncludePatterns
		c.excludePatterns = d.Spec.Filters.ExcludePatterns
	}
	return c
}

// ValidateCreate validates a v1alpha2 MCPDiscoverySource on creation.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateCreate(_ context.Context, obj *mcpv1alpha2.MCPDiscoverySource) (admission.Warnings, error) {
	if obj == nil {
		return nil, fmt.Errorf("MCPDiscoverySource object is nil")
	}
	return nil, validateDiscoveryConstraints(discoveryConstraintsFromV1alpha2(obj))
}

// ValidateUpdate validates a v1alpha2 MCPDiscoverySource on update.
//
// A cross-namespace configMapRef stored before #234 is still admitted on
// update when it is unchanged, with a warning, and so is any update of an
// object being deleted: otherwise label, annotation and finalizer updates on
// such a source -- including the controller removing its finalizer -- would be
// rejected. The controller refuses to sync it either way.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateUpdate(_ context.Context, oldObj, newObj *mcpv1alpha2.MCPDiscoverySource) (admission.Warnings, error) {
	if newObj == nil {
		return nil, fmt.Errorf("MCPDiscoverySource object is nil")
	}
	c := discoveryConstraintsFromV1alpha2(newObj)
	if c.crossNamespace() && oldObj != nil &&
		(newObj.DeletionTimestamp != nil || sameConfigMapRef(oldObj, newObj)) {
		warning := crossNamespaceMessage(c) + "; the controller does not sync this source"
		c.configMapNamespace = ""
		return admission.Warnings{warning}, validateDiscoveryConstraints(c)
	}
	return nil, validateDiscoveryConstraints(c)
}

// sameConfigMapRef reports whether an update leaves spec.configMapRef as it was.
func sameConfigMapRef(oldObj, newObj *mcpv1alpha2.MCPDiscoverySource) bool {
	o, n := oldObj.Spec.ConfigMapRef, newObj.Spec.ConfigMapRef
	return o != nil && n != nil && *o == *n && oldObj.Spec.Type == newObj.Spec.Type
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPDiscoverySourceV1alpha2Validator) ValidateDelete(_ context.Context, _ *mcpv1alpha2.MCPDiscoverySource) (admission.Warnings, error) {
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

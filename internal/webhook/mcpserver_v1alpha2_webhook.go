package webhook

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservers,verbs=create;update,versions=v1alpha2,name=vmcpserver-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPServerV1alpha2Validator validates v1alpha2 MCPServer resources on create
// and update. v1alpha2 is both the storage and a served version, so writes
// submitted at v1alpha2 must be validated here (they never reach the v1alpha1
// validator). It implements admission.CustomValidator from controller-runtime.
type MCPServerV1alpha2Validator struct{}

var _ admission.CustomValidator = &MCPServerV1alpha2Validator{}

// ValidateCreate validates a v1alpha2 MCPServer on creation.
func (v *MCPServerV1alpha2Validator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	provider, ok := obj.(*mcpv1alpha2.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha2 MCPServer, got %T", obj)
	}
	return validateProviderV2(provider)
}

// ValidateUpdate validates a v1alpha2 MCPServer on update.
func (v *MCPServerV1alpha2Validator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	provider, ok := newObj.(*mcpv1alpha2.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected v1alpha2 MCPServer, got %T", newObj)
	}
	return validateProviderV2(provider)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerV1alpha2Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateProviderV2 runs all validation rules on a v1alpha2 MCPServer.
//
// Unlike v1alpha1 (where durations are free-form strings), v1alpha2 models
// durations as *metav1.Duration, so the apiserver already rejects unparseable
// values structurally. The remaining semantic check is that a duration must not
// be negative.
func validateProviderV2(p *mcpv1alpha2.MCPServer) (admission.Warnings, error) {
	var errs []string
	var warnings admission.Warnings

	// Mode-specific field requirements.
	switch p.Spec.Mode {
	case mcpv1alpha2.MCPServerModeContainer:
		if p.Spec.Image == "" {
			errs = append(errs, "spec.image is required when mode is \"container\"")
		}
		if p.Spec.Endpoint != "" {
			warnings = append(warnings, "spec.endpoint is ignored when mode is \"container\"")
		}
	case mcpv1alpha2.MCPServerModeRemote:
		if p.Spec.Endpoint == "" {
			errs = append(errs, "spec.endpoint is required when mode is \"remote\"")
		}
		if p.Spec.Image != "" {
			warnings = append(warnings, "spec.image is ignored when mode is \"remote\"")
		}
		if p.Spec.Endpoint != "" {
			if _, err := url.ParseRequestURI(p.Spec.Endpoint); err != nil {
				errs = append(errs, fmt.Sprintf("spec.endpoint is not a valid URL: %v", err))
			}
		}
	}

	// Duration fields: reject negative values.
	durationFields := map[string]*metav1.Duration{
		"spec.idleTTL":             p.Spec.IdleTTL,
		"spec.startupTimeout":      p.Spec.StartupTimeout,
		"spec.shutdownGracePeriod": p.Spec.ShutdownGracePeriod,
	}
	if p.Spec.HealthCheck != nil {
		durationFields["spec.healthCheck.interval"] = p.Spec.HealthCheck.Interval
		durationFields["spec.healthCheck.timeout"] = p.Spec.HealthCheck.Timeout
	}
	if p.Spec.CircuitBreaker != nil {
		durationFields["spec.circuitBreaker.resetTimeout"] = p.Spec.CircuitBreaker.ResetTimeout
	}
	for field, val := range durationFields {
		if val == nil {
			continue
		}
		if val.Duration < 0 {
			errs = append(errs, fmt.Sprintf("%s must not be negative", field))
		}
	}

	// Tools: allowList and denyList are mutually exclusive.
	if p.Spec.Tools != nil && len(p.Spec.Tools.AllowList) > 0 && len(p.Spec.Tools.DenyList) > 0 {
		errs = append(errs, "spec.tools.allowList and spec.tools.denyList are mutually exclusive")
	}

	// Capabilities validation.
	if p.Spec.Capabilities != nil {
		capErrs, capWarnings := validateCapabilitiesV2(p.Spec.Capabilities)
		errs = append(errs, capErrs...)
		warnings = append(warnings, capWarnings...)
	}

	if len(errs) > 0 {
		return warnings, fmt.Errorf("MCPServer validation failed: %s", strings.Join(errs, "; "))
	}
	return warnings, nil
}

// validateCapabilitiesV2 validates the v1alpha2 capabilities block. It returns
// hard validation errors and non-fatal admission warnings.
func validateCapabilitiesV2(caps *mcpv1alpha2.MCPServerCapabilities) ([]string, admission.Warnings) {
	var errs []string
	var warnings admission.Warnings

	// Egress rules. In v1alpha2 the schema requires host (MinLength=1), but a
	// host/FQDN-only rule (no CIDR) cannot be enforced by the NetworkPolicy
	// backend, which matches only on IP/CIDR. It is failed closed rather than
	// downgraded into an all-destinations opening. Warn so the operator knows the
	// rule is inert until the Tetragon backend (ADR-006 v1.5) enforces it.
	if caps.Network != nil {
		for i, rule := range caps.Network.Egress {
			if rule.Host == "" && rule.CIDR == "" {
				errs = append(errs, fmt.Sprintf("spec.capabilities.network.egress[%d]: host or cidr must be set", i))
				continue
			}
			if rule.CIDR == "" {
				warnings = append(warnings, fmt.Sprintf(
					"spec.capabilities.network.egress[%d] (host %q) is not enforceable by the NetworkPolicy backend and will NOT be applied; specify a cidr for network-level enforcement. FQDN egress enforcement is deferred to the Tetragon backend (ADR-006 v1.5).",
					i, rule.Host))
			}
		}
	}

	// Duplicate / empty expected tools.
	if caps.Tools != nil && len(caps.Tools.ExpectedTools) > 0 {
		seen := make(map[string]bool, len(caps.Tools.ExpectedTools))
		for _, tool := range caps.Tools.ExpectedTools {
			if tool == "" {
				errs = append(errs, "spec.capabilities.tools.expectedTools contains empty string")
				continue
			}
			if seen[tool] {
				errs = append(errs, fmt.Sprintf("spec.capabilities.tools.expectedTools has duplicate: %q", tool))
			}
			seen[tool] = true
		}
	}

	return errs, warnings
}

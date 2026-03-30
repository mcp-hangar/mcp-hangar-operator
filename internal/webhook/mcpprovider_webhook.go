// Package webhook provides validating admission webhooks for MCP Hangar CRDs.
package webhook

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
)

// MCPProviderValidator validates MCPProvider resources on create and update.
// It implements admission.CustomValidator from controller-runtime.
type MCPProviderValidator struct{}

var _ admission.CustomValidator = &MCPProviderValidator{}

// ValidateCreate validates an MCPProvider on creation.
func (v *MCPProviderValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	provider, ok := obj.(*mcpv1alpha1.MCPProvider)
	if !ok {
		return nil, fmt.Errorf("expected MCPProvider, got %T", obj)
	}
	return validateProvider(provider)
}

// ValidateUpdate validates an MCPProvider on update.
func (v *MCPProviderValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	provider, ok := newObj.(*mcpv1alpha1.MCPProvider)
	if !ok {
		return nil, fmt.Errorf("expected MCPProvider, got %T", newObj)
	}
	return validateProvider(provider)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPProviderValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateProvider runs all validation rules on an MCPProvider.
func validateProvider(p *mcpv1alpha1.MCPProvider) (admission.Warnings, error) {
	var errs []string
	var warnings admission.Warnings

	// Mode-specific field requirements
	switch p.Spec.Mode {
	case mcpv1alpha1.ProviderModeContainer:
		if p.Spec.Image == "" {
			errs = append(errs, "spec.image is required when mode is \"container\"")
		}
		if p.Spec.Endpoint != "" {
			warnings = append(warnings, "spec.endpoint is ignored when mode is \"container\"")
		}
	case mcpv1alpha1.ProviderModeRemote:
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

	// Duration fields
	durationFields := map[string]string{
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

	// Tools: allowList and denyList are mutually exclusive
	if p.Spec.Tools != nil && len(p.Spec.Tools.AllowList) > 0 && len(p.Spec.Tools.DenyList) > 0 {
		errs = append(errs, "spec.tools.allowList and spec.tools.denyList are mutually exclusive")
	}

	// Capabilities validation
	if p.Spec.Capabilities != nil {
		capErrs := validateCapabilities(p.Spec.Capabilities)
		errs = append(errs, capErrs...)
	}

	if len(errs) > 0 {
		return warnings, fmt.Errorf("MCPProvider validation failed: %s", strings.Join(errs, "; "))
	}
	return warnings, nil
}

// validateCapabilities validates the capabilities block.
func validateCapabilities(caps *mcpv1alpha1.ProviderCapabilities) []string {
	var errs []string

	// Egress rules: CIDR or host must be set (not both empty)
	if caps.Network != nil {
		for i, rule := range caps.Network.Egress {
			if rule.Host == "" && rule.CIDR == "" {
				errs = append(errs, fmt.Sprintf("spec.capabilities.network.egress[%d]: host or cidr must be set", i))
			}
		}
	}

	// Duplicate expected tools
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

	return errs
}

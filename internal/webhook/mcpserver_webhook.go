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

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha1-mcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservers,verbs=create;update,versions=v1alpha1,name=vmcpserver-v1alpha1.kb.io,admissionReviewVersions=v1

// MCPServerValidator validates v1alpha1 MCPServer resources on create and update.
// It implements admission.CustomValidator from controller-runtime.
type MCPServerValidator struct{}

var _ admission.CustomValidator = &MCPServerValidator{}

// ValidateCreate validates an MCPServer on creation.
func (v *MCPServerValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	provider, ok := obj.(*mcpv1alpha1.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected MCPServer, got %T", obj)
	}
	return validateProvider(provider)
}

// ValidateUpdate validates an MCPServer on update.
func (v *MCPServerValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	provider, ok := newObj.(*mcpv1alpha1.MCPServer)
	if !ok {
		return nil, fmt.Errorf("expected MCPServer, got %T", newObj)
	}
	return validateProvider(provider)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateProvider runs all validation rules on an MCPServer.
func validateProvider(p *mcpv1alpha1.MCPServer) (admission.Warnings, error) {
	// A typed-nil *MCPServer satisfies the ok type assertion in the handlers,
	// so guard here rather than dereferencing p.Spec and panicking the webhook.
	if p == nil {
		return nil, fmt.Errorf("MCPServer object is nil")
	}

	var errs []string
	var warnings admission.Warnings

	// Mode-specific field requirements
	switch p.Spec.Mode {
	case mcpv1alpha1.MCPServerModeContainer:
		if p.Spec.Image == "" {
			errs = append(errs, "spec.image is required when mode is \"container\"")
		}
		if e, w := checkImageDigest(p.Spec.Image, p.Annotations); e != "" {
			errs = append(errs, e)
		} else if w != "" {
			warnings = append(warnings, w)
		}
		if p.Spec.Endpoint != "" {
			warnings = append(warnings, "spec.endpoint is ignored when mode is \"container\"")
		}
	case mcpv1alpha1.MCPServerModeRemote:
		if p.Spec.Endpoint == "" {
			errs = append(errs, "spec.endpoint is required when mode is \"remote\"")
		}
		if p.Spec.Image != "" {
			warnings = append(warnings, "spec.image is ignored when mode is \"remote\"")
		}
		if p.Spec.Endpoint != "" {
			if err := validateRemoteEndpoint(p.Spec.Endpoint); err != nil {
				errs = append(errs, err.Error())
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
	errs = append(errs, validateDurationStrings(durationFields)...)

	// Tools: allowList and denyList are mutually exclusive
	if p.Spec.Tools != nil && len(p.Spec.Tools.AllowList) > 0 && len(p.Spec.Tools.DenyList) > 0 {
		errs = append(errs, "spec.tools.allowList and spec.tools.denyList are mutually exclusive")
	}

	// Capabilities validation
	if p.Spec.Capabilities != nil {
		capErrs, capWarnings := validateCapabilities(p.Spec.Capabilities)
		errs = append(errs, capErrs...)
		warnings = append(warnings, capWarnings...)
	}

	// Wildcard egress (host: "*") is unrestricted egress and requires an
	// explicit opt-in annotation. This rule previously lived as a CRD
	// x-kubernetes-validations CEL expression, but CEL in a CRD cannot read
	// self.metadata.annotations (only name/generateName), so the apiserver
	// rejected the whole CRD at install on recent Kubernetes. Enforced here
	// instead, where annotations are available; this webhook is
	// failurePolicy: Fail, so it does not fail open. See #54.
	if hasWildcardEgress(p) && p.Annotations[unrestrictedEgressAnnotation] != "true" {
		errs = append(errs, fmt.Sprintf(
			"spec.capabilities.network.egress with host \"*\" (unrestricted egress) requires annotation %s: \"true\"",
			unrestrictedEgressAnnotation))
	}

	if len(errs) > 0 {
		return warnings, fmt.Errorf("MCPServer validation failed: %s", strings.Join(errs, "; "))
	}
	return warnings, nil
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

// validateRemoteEndpoint checks that a remote MCPServer endpoint is an absolute
// http(s) URL with a non-empty host. url.ParseRequestURI alone accepts
// non-HTTP schemes (e.g. "javascript:alert(1)") and bare paths ("/only/path"),
// neither of which is a reachable remote endpoint.
func validateRemoteEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("spec.endpoint is not a valid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("spec.endpoint must be an http or https URL, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("spec.endpoint must include a host")
	}
	return nil
}

// validateCapabilities validates the capabilities block. It returns hard
// validation errors and non-fatal admission warnings.
func validateCapabilities(caps *mcpv1alpha1.MCPServerCapabilities) ([]string, admission.Warnings) {
	var errs []string
	var warnings admission.Warnings

	// Egress rules: CIDR or host must be set (not both empty)
	if caps.Network != nil {
		for i, rule := range caps.Network.Egress {
			if rule.Host == "" && rule.CIDR == "" {
				errs = append(errs, fmt.Sprintf("spec.capabilities.network.egress[%d]: host or cidr must be set", i))
				continue
			}
			// A host/FQDN-only rule (no CIDR) cannot be enforced by the
			// NetworkPolicy backend, which matches only on IP/CIDR. It is
			// failed closed (the port is NOT opened) rather than downgraded
			// into an all-destinations opening. Warn so the operator knows the
			// rule is inert until the Tetragon backend (ADR-006 v1.5) enforces
			// it.
			if rule.CIDR == "" {
				warnings = append(warnings, fmt.Sprintf(
					"spec.capabilities.network.egress[%d] (host %q) is not enforceable by the NetworkPolicy backend and will NOT be applied; specify a cidr for network-level enforcement. FQDN egress enforcement is deferred to the Tetragon backend (ADR-006 v1.5).",
					i, rule.Host))
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

	return errs, warnings
}

// unrestrictedEgressAnnotation opts a provider into wildcard (host: "*") egress.
const unrestrictedEgressAnnotation = "hangar.io/allow-unrestricted-egress"

// hasWildcardEgress reports whether any egress rule targets host "*".
func hasWildcardEgress(p *mcpv1alpha1.MCPServer) bool {
	if p.Spec.Capabilities == nil || p.Spec.Capabilities.Network == nil {
		return false
	}
	for _, rule := range p.Spec.Capabilities.Network.Egress {
		if rule.Host == "*" {
			return true
		}
	}
	return false
}

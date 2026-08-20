package webhook

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// +kubebuilder:webhook:path=/validate-mcp-hangar-io-v1alpha2-mcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=mcp-hangar.io,resources=mcpservers,verbs=create;update,versions=v1alpha2,name=vmcpserver-v1alpha2.kb.io,admissionReviewVersions=v1

// MCPServerV1alpha2Validator validates v1alpha2 MCPServer resources on create
// and update. v1alpha2 is both the storage and a served version, so writes
// submitted at v1alpha2 must be validated here (they never reach the v1alpha1
// validator). It implements admission.Validator[*MCPServer] from
// controller-runtime.
type MCPServerV1alpha2Validator struct{}

var _ admission.Validator[*mcpv1alpha2.MCPServer] = &MCPServerV1alpha2Validator{}

// ValidateCreate validates a v1alpha2 MCPServer on creation.
func (v *MCPServerV1alpha2Validator) ValidateCreate(_ context.Context, obj *mcpv1alpha2.MCPServer) (admission.Warnings, error) {
	return validateProviderV2(obj)
}

// ValidateUpdate validates a v1alpha2 MCPServer on update.
func (v *MCPServerV1alpha2Validator) ValidateUpdate(_ context.Context, _, newObj *mcpv1alpha2.MCPServer) (admission.Warnings, error) {
	return validateProviderV2(newObj)
}

// ValidateDelete is a no-op; deletion is always allowed.
func (v *MCPServerV1alpha2Validator) ValidateDelete(_ context.Context, _ *mcpv1alpha2.MCPServer) (admission.Warnings, error) {
	return nil, nil
}

// validateProviderV2 runs all validation rules on a v1alpha2 MCPServer.
//
// Unlike v1alpha1 (where durations are free-form strings), v1alpha2 models
// durations as *metav1.Duration, so the apiserver already rejects unparseable
// values structurally. The remaining semantic check is that a duration must not
// be negative.
func validateProviderV2(p *mcpv1alpha2.MCPServer) (admission.Warnings, error) {
	// A typed-nil *MCPServer satisfies the generic handler signature, so guard
	// here rather than dereferencing p.Spec and panicking the webhook.
	if p == nil {
		return nil, fmt.Errorf("MCPServer object is nil")
	}

	var errs []string
	var warnings admission.Warnings

	// Mode-specific field requirements.
	switch p.Spec.Mode {
	case mcpv1alpha2.MCPServerModeContainer:
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
	case mcpv1alpha2.MCPServerModeRemote:
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

	// Duration fields: reject negative values.
	durationFields := map[string]*metav1.Duration{
		"spec.startupTimeout":      p.Spec.StartupTimeout,
		"spec.shutdownGracePeriod": p.Spec.ShutdownGracePeriod,
	}
	for field, val := range durationFields {
		if val == nil {
			continue
		}
		if val.Duration < 0 {
			errs = append(errs, fmt.Sprintf("%s must not be negative", field))
		}
	}

	// Capabilities validation.
	if p.Spec.Capabilities != nil {
		capErrs, capWarnings := validateCapabilitiesV2(p.Spec.Capabilities)
		errs = append(errs, capErrs...)
		warnings = append(warnings, capWarnings...)
	}

	// Wildcard egress needs the explicit opt-in annotation. Ported from the
	// v1alpha1 validator when that one was deleted (#125) -- this check lived
	// only there, so without the port the guard would have died with the API
	// version while looking covered.
	if hasWildcardEgressV2(p) && p.Annotations[unrestrictedEgressAnnotation] != "true" {
		errs = append(errs, fmt.Sprintf(
			"spec.capabilities.network.egress with host \"*\" (unrestricted egress) requires annotation %s: \"true\"",
			unrestrictedEgressAnnotation))
	}

	if len(errs) > 0 {
		return warnings, fmt.Errorf("MCPServer validation failed: %s", strings.Join(errs, "; "))
	}
	return warnings, nil
}

// unrestrictedEgressAnnotation opts a provider into wildcard (host: "*") egress.
const unrestrictedEgressAnnotation = "hangar.io/allow-unrestricted-egress"

// ciliumDetected records whether this cluster runs Cilium (its CRD is
// installed). Set once at operator startup via SetCiliumDetected; false means
// "no Cilium detected", which is also the right answer when webhooks run
// outside a cluster (unit tests). Gates the #152 CIDR warning so Calico and
// other CNIs -- which do honour an ipBlock naming a pod IP -- stay quiet.
var ciliumDetected bool

// SetCiliumDetected records whether the cluster runs Cilium.
func SetCiliumDetected(detected bool) {
	ciliumDetected = detected
}

// hasWildcardEgressV2 reports whether any egress rule targets host "*".
func hasWildcardEgressV2(p *mcpv1alpha2.MCPServer) bool {
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
				continue
			}
			// #152: a CIDR rule naming an in-cluster upstream is accepted by the
			// apiserver and dropped on the wire under stock Cilium. Warn (never
			// reject): whether policyCIDRMatchMode is set is not readable from
			// here, and the range test cannot tell an in-cluster pod IP from a
			// legitimately private external upstream.
			if ciliumDetected && networkpolicy.LooksClusterInternal(rule.CIDR) {
				warnings = append(warnings, fmt.Sprintf(
					"spec.capabilities.network.egress[%d] (cidr %q): %s",
					i, rule.CIDR, networkpolicy.CiliumCIDRMatchNote))
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

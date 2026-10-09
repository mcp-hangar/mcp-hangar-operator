package webhook

import (
	"context"
	"fmt"
	"strings"

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

// validateProviderV2 runs the rules on a v1alpha2 MCPServer that the CRD schema
// cannot express.
//
// The structural rules -- image required in container mode, an http(s)
// endpoint with a host in remote mode, non-negative durations, non-empty and
// unique expectedTools, a well-formed cidr, mode immutable -- are CEL and
// schema rules on the CRD (#196). The apiserver evaluates them before any
// validating webhook is called, and with the webhook off as well, so a copy
// here could never fire. What remains needs an annotation or a cluster fact:
// the image digest policy, the wildcard egress opt-in, and the warnings.
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
		if e, w := checkImageDigest(p.Spec.Image, p.Annotations); e != "" {
			errs = append(errs, e)
		} else if w != "" {
			warnings = append(warnings, w)
		}
		if p.Spec.Endpoint != "" {
			warnings = append(warnings, "spec.endpoint is ignored when mode is \"container\"")
		}
	case mcpv1alpha2.MCPServerModeRemote:
		if p.Spec.Image != "" {
			warnings = append(warnings, "spec.image is ignored when mode is \"remote\"")
		}
	}

	// Capabilities warnings.
	if p.Spec.Capabilities != nil {
		warnings = append(warnings, capabilityWarningsV2(p.Spec.Capabilities)...)
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

// capabilityWarningsV2 returns the non-fatal admission warnings for the
// v1alpha2 capabilities block. Its hard rules are CRD schema rules (#196).
func capabilityWarningsV2(caps *mcpv1alpha2.MCPServerCapabilities) admission.Warnings {
	var warnings admission.Warnings

	// Egress rules. In v1alpha2 the schema requires host (MinLength=1), but a
	// host/FQDN-only rule (no CIDR) cannot be enforced by the NetworkPolicy
	// backend, which matches only on IP/CIDR. It is failed closed rather than
	// downgraded into an all-destinations opening. Warn so the operator knows the
	// rule is inert until the Tetragon backend (ADR-006 v1.5) enforces it.
	if caps.Network != nil {
		for i, rule := range caps.Network.Egress {
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

	return warnings
}

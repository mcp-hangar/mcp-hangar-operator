/*
Package v1alpha2 contains the MCPEgressPolicy API.

MCPEgressPolicy is the declarative policy layer above the binary registration
switch delivered by the enforcement roadmap (default-deny egress, admission
rejection, image-pin coupling). It answers not just "may this server receive
traffic at all?" but "which tool calls, with which arguments, to which
upstreams -- and what happens when the answer is no." The enforcement model
(explicit-proxy L7 + a policy-generated network backstop; no TLS interception
or eBPF protocol parsing in v1) is fixed by ADR-013.

This file defines the API type only. The reconciler that compiles a policy into
a data-plane document + network backstop, and the core-side L7 enforcement, land
in follow-up changes (epic #53).
*/
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EgressPolicyMode controls whether violations are only observed or actively
// blocked. Audit is the default so a policy can be rolled out and its effect
// observed (Gatekeeper-style adoption) before it starts denying traffic.
type EgressPolicyMode string

const (
	// EgressPolicyModeAudit records violations without blocking.
	EgressPolicyModeAudit EgressPolicyMode = "Audit"
	// EgressPolicyModeEnforce blocks traffic that violates the policy.
	EgressPolicyModeEnforce EgressPolicyMode = "Enforce"
)

// EgressPolicyAction is the decision applied to traffic not matched by any
// upstream rule.
type EgressPolicyAction string

const (
	// EgressActionDeny denies unmatched traffic (deny-by-default).
	EgressActionDeny EgressPolicyAction = "Deny"
	// EgressActionAllow allows unmatched traffic (audit/observability posture).
	EgressActionAllow EgressPolicyAction = "Allow"
)

// BackstopFlavor selects how the L3/L4 network backstop is generated.
type BackstopFlavor string

const (
	// BackstopFlavorAuto detects the CNI (Cilium CRDs present -> Cilium, else Vanilla).
	BackstopFlavorAuto BackstopFlavor = "Auto"
	// BackstopFlavorCilium emits a CiliumNetworkPolicy with toFQDNs.
	BackstopFlavorCilium BackstopFlavor = "Cilium"
	// BackstopFlavorVanilla emits a default-deny egress NetworkPolicy plus
	// allow-to-Hangar and allow-DNS in governed namespaces.
	BackstopFlavorVanilla BackstopFlavor = "Vanilla"
)

// EgressTargetRef attaches a policy to a server or a group of servers, never to
// raw pods.
type EgressTargetRef struct {
	// Kind is the referent type.
	// +kubebuilder:validation:Enum=MCPServer;MCPServerGroup
	Kind string `json:"kind"`

	// Name is the referent name, resolved in the policy's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// UpstreamMatch selects an upstream and the trust conditions under which it may
// be reached. Digest/issuer fields reference primitives that already exist
// elsewhere rather than duplicating them.
type UpstreamMatch struct {
	// Host is the upstream hostname (FQDN) this rule matches.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host"`

	// ToolSchemaDigestRef references an existing per-tenant tool-schema pin.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	ToolSchemaDigestRef string `json:"toolSchemaDigestRef,omitempty"`

	// ImageDigest controls how the target's container image pin gates this
	// upstream: "required" demands a pinned image, "inherited" defers to the
	// operator-wide image-digest policy.
	// +kubebuilder:validation:Enum=required;inherited
	// +optional
	ImageDigest string `json:"imageDigest,omitempty"`

	// Issuers restricts which token issuers may be brokered to this upstream.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Issuers []string `json:"issuers,omitempty"`
}

// ToolRules matches MCP tool calls by glob on tool name. A call may be allowed,
// denied, or routed to the existing human-in-the-loop approval gates.
type ToolRules struct {
	// Allow lists tool-name globs permitted to this upstream.
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Allow []string `json:"allow,omitempty"`

	// Deny lists tool-name globs rejected outright.
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Deny []string `json:"deny,omitempty"`

	// RequireApproval lists tool-name globs that route into approval gates.
	// +kubebuilder:validation:MaxItems=128
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// ArgumentRules constrains tool-call arguments. Detection is deterministic by
// design: pattern and size limits only -- full DLP and ML-based detection are
// explicit non-goals (ADR-013).
type ArgumentRules struct {
	// Deny rejects tool calls whose arguments match the given constraints.
	// +optional
	Deny *ArgumentDenyRules `json:"deny,omitempty"`
}

// ArgumentDenyRules are the deterministic argument constraints.
type ArgumentDenyRules struct {
	// SecretPatterns names deterministic secret-detection patterns to reject
	// (e.g. aws-keys, pem-blocks, jwt).
	// +kubebuilder:validation:MaxItems=64
	// +optional
	SecretPatterns []string `json:"secretPatterns,omitempty"`

	// MaxPayloadBytes rejects tool-call argument payloads larger than this.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxPayloadBytes int64 `json:"maxPayloadBytes,omitempty"`
}

// UpstreamRule is one entry in the deny-by-default allow-list.
type UpstreamRule struct {
	// Name identifies the rule (unique within the policy).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Match selects the upstream and its trust conditions.
	Match UpstreamMatch `json:"match"`

	// Tools constrains which tool calls may reach this upstream.
	// +optional
	Tools *ToolRules `json:"tools,omitempty"`

	// Arguments constrains tool-call arguments to this upstream.
	// +optional
	Arguments *ArgumentRules `json:"arguments,omitempty"`
}

// NetworkBackstop controls generation of the L3/L4 backstop that prevents the
// data plane from being bypassed. A policy without the backstop is a
// suggestion, not enforcement (ADR-013).
type NetworkBackstop struct {
	// Generate emits the network backstop alongside the compiled policy.
	// +kubebuilder:default=true
	Generate bool `json:"generate"`

	// Flavor selects the backstop implementation.
	// +kubebuilder:validation:Enum=Auto;Cilium;Vanilla
	// +kubebuilder:default=Auto
	// +optional
	Flavor BackstopFlavor `json:"flavor,omitempty"`
}

// MCPEgressPolicySpec is the desired egress policy.
// +kubebuilder:validation:XValidation:rule="self.upstreams.all(u1, self.upstreams.exists_one(u2, u2.name == u1.name))",message="upstream names must be unique"
type MCPEgressPolicySpec struct {
	// Mode controls whether violations are only observed (Audit) or blocked
	// (Enforce).
	// +kubebuilder:validation:Enum=Audit;Enforce
	// +kubebuilder:default=Audit
	// +optional
	Mode EgressPolicyMode `json:"mode,omitempty"`

	// TargetRef is the server or group this policy governs.
	TargetRef EgressTargetRef `json:"targetRef"`

	// DefaultAction is applied to traffic not matched by any upstream rule.
	// +kubebuilder:validation:Enum=Deny;Allow
	// +kubebuilder:default=Deny
	// +optional
	DefaultAction EgressPolicyAction `json:"defaultAction,omitempty"`

	// Upstreams is the allow-list. With DefaultAction=Deny (the default), an
	// empty list denies all egress except the always-permitted DNS/Hangar
	// backstop paths.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Upstreams []UpstreamRule `json:"upstreams,omitempty"`

	// NetworkBackstop controls the generated L3/L4 backstop. Defaults to
	// generating an auto-detected backstop when omitted.
	// +optional
	NetworkBackstop *NetworkBackstop `json:"networkBackstop,omitempty"`
}

// MCPEgressPolicyStatus is the observed state.
type MCPEgressPolicyStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions capture policy state: Compiled, BackstopApplied, and
	// Degraded (with reason FailOpenRisk when the backstop could not be applied).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Default",type=string,JSONPath=`.spec.defaultAction`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=mcpep;egresspolicy,categories=mcp

// MCPEgressPolicy is the Schema for the mcpegresspolicies API.
type MCPEgressPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPEgressPolicySpec   `json:"spec,omitempty"`
	Status MCPEgressPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPEgressPolicyList contains a list of MCPEgressPolicy.
type MCPEgressPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPEgressPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPEgressPolicy{}, &MCPEgressPolicyList{})
}

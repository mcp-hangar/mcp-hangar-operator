// Package v1alpha2 contains API Schema definitions for the mcp-hangar.io v1alpha2 API group
package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPServerMode defines the execution mode for a provider
// +kubebuilder:validation:Enum=container;remote
type MCPServerMode string

const (
	// MCPServerModeContainer runs the provider as a Kubernetes Pod
	MCPServerModeContainer MCPServerMode = "container"
	// MCPServerModeRemote connects to an external HTTP endpoint
	MCPServerModeRemote MCPServerMode = "remote"
)

// MCPServerState represents the current state of a provider
// +kubebuilder:validation:Enum=Cold;Initializing;Ready;Degraded;Dead
type MCPServerState string

const (
	MCPServerStateCold         MCPServerState = "Cold"
	MCPServerStateInitializing MCPServerState = "Initializing"
	MCPServerStateReady        MCPServerState = "Ready"
	MCPServerStateDegraded     MCPServerState = "Degraded"
	MCPServerStateDead         MCPServerState = "Dead"
)

// MaxViolationRecords is the maximum number of violation records kept in status.
// Prevents CRD status size explosion (etcd ~1.5MB limit).
const MaxViolationRecords = 100

// MCPServerSpec defines the desired state of MCPServer
type MCPServerSpec struct {
	// Mode is the provider execution mode (container or remote)
	// +kubebuilder:validation:Required
	Mode MCPServerMode `json:"mode"`

	// Image is the container image for the provider (required for container mode)
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Image string `json:"image,omitempty"`

	// Command overrides the container entrypoint
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are arguments to the entrypoint
	// +optional
	Args []string `json:"args,omitempty"`

	// WorkingDir is the container working directory
	// +optional
	WorkingDir string `json:"workingDir,omitempty"`

	// Endpoint is the HTTP endpoint for remote providers (required for remote
	// mode): an absolute http or https URL with a host.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Replicas switches the server on (1) or off (0). A server runs at most one
	// pod; values above 1 were accepted before and silently ran one (#192).
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// StartupTimeout is the maximum time to wait for provider startup.
	// Uses standard Kubernetes duration format (e.g. "30s").
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="must be a non-negative duration such as 30s"
	// +optional
	StartupTimeout *metav1.Duration `json:"startupTimeout,omitempty"`

	// ShutdownGracePeriod is the grace period for graceful shutdown.
	// Uses standard Kubernetes duration format (e.g. "30s").
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="must be a non-negative duration such as 30s"
	// +optional
	ShutdownGracePeriod *metav1.Duration `json:"shutdownGracePeriod,omitempty"`

	// Resources defines resource requirements for the provider container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Env defines environment variables for the provider container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Volumes are the pod's volumes. Mount them with volumeMounts.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts mounts volumes into the provider container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// PodSecurityContext is the pod-level security context. Unset means the
	// operator's restricted defaults.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// ContainerSecurityContext is the provider container's security context.
	// Unset means the operator's restricted defaults.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	// ServiceAccountName is the ServiceAccount for the provider pod
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// AutomountServiceAccountToken controls whether the provider pod gets a
	// ServiceAccount token projected into it. Unset means false: an MCP server
	// is a workload whose premise is least-privilege egress, and a bearer token
	// for the API server on disk is a credential most of them never use (#207).
	// Set true only when the server needs the API, usually together with a
	// dedicated serviceAccountName.
	// +optional
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken,omitempty"`

	// ImagePullSecrets for pulling the container image
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// NodeSelector for pod scheduling
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for pod scheduling
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity rules for pod scheduling
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// PriorityClassName for pod scheduling priority
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// Capabilities declares what resources the MCP server needs.
	// Used by the operator for NetworkPolicy generation and enforcement.
	// +optional
	Capabilities *MCPServerCapabilities `json:"capabilities,omitempty"`
}

// MCPServerCapabilities declares what resources an MCP server needs.
// The operator uses this to generate NetworkPolicy, enforce Pod Security Standards,
// and verify runtime behavior matches declarations.
type MCPServerCapabilities struct {
	// Network defines allowed network access
	// +optional
	Network *NetworkCapabilitiesSpec `json:"network,omitempty"`

	// Tools defines tool schema constraints
	// +optional
	Tools *ToolCapabilitiesSpec `json:"tools,omitempty"`

	// EnforcementMode controls how violations are handled: alert, block, or quarantine.
	// +kubebuilder:validation:Enum=alert;block;quarantine
	// +kubebuilder:default=alert
	// +optional
	EnforcementMode string `json:"enforcementMode,omitempty"`
}

// NetworkCapabilitiesSpec declares network access requirements
type NetworkCapabilitiesSpec struct {
	// Egress is the list of allowed outbound destinations
	// +optional
	Egress []EgressRuleSpec `json:"egress,omitempty"`

	// DNSAllowed controls whether DNS queries are permitted
	// +kubebuilder:default=true
	// +optional
	DNSAllowed *bool `json:"dnsAllowed,omitempty"`

	// LoopbackAllowed controls whether localhost connections are permitted
	// +optional
	LoopbackAllowed *bool `json:"loopbackAllowed,omitempty"`
}

// EgressRuleSpec defines a single allowed outbound destination
type EgressRuleSpec struct {
	// Host is a hostname or glob pattern (e.g. "api.example.com" or "*.internal.corp")
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Port is the TCP port (0 = any port)
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=443
	// +optional
	Port int32 `json:"port,omitempty"`

	// Protocol is the application protocol hint
	// +kubebuilder:validation:Enum=https;http;grpc;tcp;any
	// +kubebuilder:default=https
	// +optional
	Protocol string `json:"protocol,omitempty"`

	// The pattern stands in for CEL isCIDR(), which a 1.30 apiserver does not
	// yet accept in a new CRD rule: it compiles new rules against the 1.29
	// library set (#196).

	// CIDR is an IP range (alternative to host, for K8s-native rules): an IPv4
	// CIDR such as 10.0.0.0/8, or an IPv6 one such as fd00::/8. The IPv4 form
	// is checked exactly, the IPv6 form only for its shape and prefix length.
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:Pattern=`^((((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])/(3[0-2]|[12]?[0-9]))|([0-9a-fA-F.]*:[0-9a-fA-F:.]*/(12[0-8]|1[01][0-9]|[1-9]?[0-9])))$`
	// +optional
	CIDR string `json:"cidr,omitempty"`
}

// ToolCapabilitiesSpec declares tool schema constraints
type ToolCapabilitiesSpec struct {
	// MaxCount is the maximum number of tools the provider may advertise (0 = unlimited)
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxCount int32 `json:"maxCount,omitempty"`

	// SchemaDriftAlert enables alerting when tool schema changes between restarts
	// +kubebuilder:default=true
	// +optional
	SchemaDriftAlert *bool `json:"schemaDriftAlert,omitempty"`

	// ExpectedTools is the list of tool names the provider is expected to expose.
	// Used for runtime drift detection: tools present at runtime but not in this
	// list trigger a schema_mismatch violation. Names must be non-empty and
	// unique.
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self.all(t, self.exists_one(u, u == t))",message="expectedTools must not contain duplicates"
	// +optional
	ExpectedTools []string `json:"expectedTools,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer
type MCPServerStatus struct {
	// State is the current provider state
	State MCPServerState `json:"state,omitempty"`

	// Replicas is the number of provider pods that exist: 0 or 1.
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready replicas
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of available replicas
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// ToolsCount is the number of exposed tools
	ToolsCount int32 `json:"toolsCount,omitempty"`

	// Tools is the list of tool names
	Tools []string `json:"tools,omitempty"`

	// Endpoint is the internal endpoint URL
	Endpoint string `json:"endpoint,omitempty"`

	// LastStartedAt is the last startup time
	LastStartedAt *metav1.Time `json:"lastStartedAt,omitempty"`

	// LastStoppedAt is the last shutdown time
	LastStoppedAt *metav1.Time `json:"lastStoppedAt,omitempty"`

	// LastHealthCheck is the last successful health check
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// ConsecutiveFailures counts consecutive health failures
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// ObservedGeneration is the generation observed by controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PodName is the name of the managed Pod
	PodName string `json:"podName,omitempty"`

	// Conditions represent the latest available observations.
	// Uses standard metav1.Condition (improved from v1alpha1 custom Condition type).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Capabilities is the observed/normalized capabilities
	// +optional
	Capabilities *MCPServerCapabilities `json:"capabilities,omitempty"`

	// Violations records detected capability violations (most recent MaxViolationRecords entries)
	// +kubebuilder:validation:MaxItems=100
	// +optional
	Violations []ViolationRecord `json:"violations,omitempty"`
}

// ViolationRecord represents a detected capability violation.
// Stored in MCPServerStatus.Violations for audit and visibility via kubectl.
type ViolationRecord struct {
	// Type of violation: egress_denied, capability_drift, undeclared_tool, schema_mismatch, quarantine_triggered
	// +kubebuilder:validation:Enum=egress_denied;capability_drift;undeclared_tool;schema_mismatch;quarantine_triggered
	Type string `json:"type"`

	// Detail is a human-readable description of the violation
	// +optional
	Detail string `json:"detail,omitempty"`

	// Severity: critical, high, medium, low
	// +kubebuilder:validation:Enum=critical;high;medium;low
	Severity string `json:"severity"`

	// Action is the enforcement action taken: alert, block, quarantine
	// +kubebuilder:validation:Enum=alert;block;quarantine
	Action string `json:"action"`

	// Destination is the network destination (for egress violations)
	// +optional
	Destination string `json:"destination,omitempty"`

	// Timestamp is when the violation was detected
	Timestamp metav1.Time `json:"timestamp"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Tools",type=integer,JSONPath=`.status.toolsCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=mcpp;provider,categories=mcp
// +kubebuilder:storageversion

// MCPServer is the Schema for the mcpservers API
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// The mode rules sit on this field, not on MCPServerSpec: the discovery
	// source's providerTemplate.spec embeds MCPServerSpec, and a template may
	// leave the image to the discovered entry. They used to live only in the
	// webhook, which is off by default, so with it off the apiserver stored a
	// container server with no image and a remote one with any endpoint (#196).

	// +kubebuilder:validation:XValidation:rule="self.mode != 'container' || (has(self.image) && size(self.image) > 0)",message="spec.image is required when mode is container"
	// +kubebuilder:validation:XValidation:rule="self.mode != 'remote' || (has(self.endpoint) && size(self.endpoint) > 0)",message="spec.endpoint is required when mode is remote"
	// +kubebuilder:validation:XValidation:rule="self.mode != 'remote' || !has(self.endpoint) || size(self.endpoint) == 0 || (isURL(self.endpoint) && url(self.endpoint).getScheme() in ['http', 'https'] && size(url(self.endpoint).getHostname()) > 0)",message="spec.endpoint must be an absolute http or https URL with a host"
	// +kubebuilder:validation:XValidation:rule="self.mode == oldSelf.mode",message="spec.mode is immutable: delete and recreate the MCPServer to change it"
	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &MCPServer{}, &MCPServerList{})
}

// Helper methods

// GetReplicas returns the number of replicas (defaults to 1)
func (p *MCPServer) GetReplicas() int32 {
	if p.Spec.Replicas == nil {
		return 1
	}
	return *p.Spec.Replicas
}

// IsCold returns true if the provider should be cold (replicas=0)
func (p *MCPServer) IsCold() bool {
	return p.GetReplicas() == 0
}

// IsContainerMode returns true if running as container
func (p *MCPServer) IsContainerMode() bool {
	return p.Spec.Mode == MCPServerModeContainer
}

// IsRemoteMode returns true if connecting to remote endpoint
func (p *MCPServer) IsRemoteMode() bool {
	return p.Spec.Mode == MCPServerModeRemote
}

// GetPodName returns the expected pod name
func (p *MCPServer) GetPodName() string {
	return "mcp-provider-" + p.Name
}

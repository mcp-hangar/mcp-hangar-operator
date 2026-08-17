// Package v1alpha1 contains API Schema definitions for the mcp-hangar.io v1alpha1 API group
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPServerGroupSpec defines the desired state of MCPServerGroup
type MCPServerGroupSpec struct {
	// Selector selects MCPServers to include in the group
	// +kubebuilder:validation:Required
	Selector *metav1.LabelSelector `json:"selector"`

	// HealthPolicy defines group health requirements
	// +optional
	HealthPolicy *HealthPolicy `json:"healthPolicy,omitempty"`
}

// HealthPolicy defines group health requirements
type HealthPolicy struct {
	// MinHealthyPercentage is minimum healthy providers percentage
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MinHealthyPercentage int32 `json:"minHealthyPercentage,omitempty"`

	// MinHealthyCount is minimum healthy provider count (overrides percentage)
	// +optional
	MinHealthyCount *int32 `json:"minHealthyCount,omitempty"`

	// UnhealthyThreshold is consecutive failures before marking unhealthy
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	UnhealthyThreshold int32 `json:"unhealthyThreshold,omitempty"`
}

// MCPServerGroupStatus defines the observed state of MCPServerGroup
type MCPServerGroupStatus struct {
	// ProviderCount is total providers in group
	ProviderCount int32 `json:"providerCount,omitempty"`

	// ReadyCount is the number of ready providers
	ReadyCount int32 `json:"readyCount,omitempty"`

	// DegradedCount is the number of degraded providers
	DegradedCount int32 `json:"degradedCount,omitempty"`

	// ColdCount is the number of cold providers
	ColdCount int32 `json:"coldCount,omitempty"`

	// DeadCount is the number of dead providers
	DeadCount int32 `json:"deadCount,omitempty"`

	// Providers contains provider member details
	Providers []MCPServerMemberStatus `json:"providers,omitempty"`

	// ObservedGeneration is the generation observed by controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations
	Conditions []Condition `json:"conditions,omitempty"`
}

// MCPServerMemberStatus defines the status of a group member
type MCPServerMemberStatus struct {
	// Name of the provider
	Name string `json:"name"`

	// Namespace of the provider
	Namespace string `json:"namespace"`

	// State of the provider
	State string `json:"state,omitempty"`

	// LastHealthCheck time
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:unservedversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Providers",type=integer,JSONPath=`.status.providerCount`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyCount`
// +kubebuilder:printcolumn:name="Degraded",type=integer,JSONPath=`.status.degradedCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=mcppg;providergroup,categories=mcp

// MCPServerGroup is the Schema for the mcpservergroups API
type MCPServerGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerGroupSpec   `json:"spec,omitempty"`
	Status MCPServerGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerGroupList contains a list of MCPServerGroup
type MCPServerGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServerGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServerGroup{}, &MCPServerGroupList{})
}

// Helper methods

// IsHealthy returns true if the group meets health requirements
func (s *MCPServerGroupStatus) IsHealthy(policy *HealthPolicy) bool {
	if s.ProviderCount == 0 {
		return false
	}

	if policy == nil {
		return s.ReadyCount > 0
	}

	// Check minimum count first
	if policy.MinHealthyCount != nil {
		return s.ReadyCount >= *policy.MinHealthyCount
	}

	// Check percentage
	percentage := (s.ReadyCount * 100) / s.ProviderCount
	return percentage >= policy.MinHealthyPercentage
}

// SetCondition sets or updates a condition
func (s *MCPServerGroupStatus) SetCondition(condType string, status metav1.ConditionStatus, reason, message string) {
	SetConditionOnSlice(&s.Conditions, condType, status, reason, message)
}

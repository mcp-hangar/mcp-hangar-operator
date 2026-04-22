// Package v1alpha1 implements Spoke conversion methods for converting between
// v1alpha1 and the v1alpha2 Hub version.
//
// Conversion rules:
//   - Duration fields: v1alpha1 uses plain strings (e.g. "5m"), v1alpha2 uses *metav1.Duration
//   - Status conditions: v1alpha1 uses a custom Condition type, v1alpha2 uses metav1.Condition
//   - MCPDiscoverySource.Status.LastSyncDuration: v1alpha1 string, v1alpha2 *metav1.Duration
//
// All other fields are structurally identical and are copied directly.
package v1alpha1

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// --- MCPServer conversion ---

// Ensure MCPServer implements conversion.Convertible
var _ conversion.Convertible = &MCPServer{}

// ConvertTo converts this v1alpha1 MCPServer to the Hub version (v1alpha2).
func (src *MCPServer) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1alpha2.MCPServer)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPServer, got %T", dstRaw)
	}

	// ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Spec: direct fields
	dst.Spec.Mode = v1alpha2.MCPServerMode(src.Spec.Mode)
	dst.Spec.Image = src.Spec.Image
	dst.Spec.Command = src.Spec.Command
	dst.Spec.Args = src.Spec.Args
	dst.Spec.WorkingDir = src.Spec.WorkingDir
	dst.Spec.Endpoint = src.Spec.Endpoint
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.ServiceAccountName = src.Spec.ServiceAccountName
	dst.Spec.ImagePullSecrets = src.Spec.ImagePullSecrets
	dst.Spec.NodeSelector = src.Spec.NodeSelector
	dst.Spec.Affinity = src.Spec.Affinity
	dst.Spec.PriorityClassName = src.Spec.PriorityClassName

	// Spec: duration fields (string -> *metav1.Duration)
	var err error
	if dst.Spec.IdleTTL, err = stringToDuration(src.Spec.IdleTTL); err != nil {
		return fmt.Errorf("converting spec.idleTTL: %w", err)
	}
	if dst.Spec.StartupTimeout, err = stringToDuration(src.Spec.StartupTimeout); err != nil {
		return fmt.Errorf("converting spec.startupTimeout: %w", err)
	}
	if dst.Spec.ShutdownGracePeriod, err = stringToDuration(src.Spec.ShutdownGracePeriod); err != nil {
		return fmt.Errorf("converting spec.shutdownGracePeriod: %w", err)
	}

	// Spec: nested structs
	if src.Spec.HealthCheck != nil {
		dst.Spec.HealthCheck = &v1alpha2.HealthCheckConfig{
			Enabled:          src.Spec.HealthCheck.Enabled,
			FailureThreshold: src.Spec.HealthCheck.FailureThreshold,
			SuccessThreshold: src.Spec.HealthCheck.SuccessThreshold,
		}
		if dst.Spec.HealthCheck.Interval, err = stringToDuration(src.Spec.HealthCheck.Interval); err != nil {
			return fmt.Errorf("converting spec.healthCheck.interval: %w", err)
		}
		if dst.Spec.HealthCheck.Timeout, err = stringToDuration(src.Spec.HealthCheck.Timeout); err != nil {
			return fmt.Errorf("converting spec.healthCheck.timeout: %w", err)
		}
	}

	if src.Spec.Resources != nil {
		dst.Spec.Resources = convertResourceRequirementsTo(src.Spec.Resources)
	}

	dst.Spec.Env = convertEnvVarsTo(src.Spec.Env)
	dst.Spec.Volumes = convertVolumesTo(src.Spec.Volumes)

	if src.Spec.SecurityContext != nil {
		dst.Spec.SecurityContext = convertSecurityContextTo(src.Spec.SecurityContext)
	}

	dst.Spec.Tolerations = convertTolerationsTo(src.Spec.Tolerations)

	if src.Spec.Tools != nil {
		dst.Spec.Tools = convertToolsConfigTo(src.Spec.Tools)
	}

	if src.Spec.CircuitBreaker != nil {
		dst.Spec.CircuitBreaker = &v1alpha2.CircuitBreakerConfig{
			Enabled:          src.Spec.CircuitBreaker.Enabled,
			FailureThreshold: src.Spec.CircuitBreaker.FailureThreshold,
			SuccessThreshold: src.Spec.CircuitBreaker.SuccessThreshold,
			HalfOpenRequests: src.Spec.CircuitBreaker.HalfOpenRequests,
		}
		if dst.Spec.CircuitBreaker.ResetTimeout, err = stringToDuration(src.Spec.CircuitBreaker.ResetTimeout); err != nil {
			return fmt.Errorf("converting spec.circuitBreaker.resetTimeout: %w", err)
		}
	}

	if src.Spec.Observability != nil {
		dst.Spec.Observability = convertObservabilityTo(src.Spec.Observability)
	}

	if src.Spec.Capabilities != nil {
		dst.Spec.Capabilities = convertMCPServerCapabilitiesTo(src.Spec.Capabilities)
	}

	// Status
	dst.Status.State = v1alpha2.MCPServerState(src.Status.State)
	dst.Status.Phase = src.Status.Phase
	dst.Status.Replicas = src.Status.Replicas
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.AvailableReplicas = src.Status.AvailableReplicas
	dst.Status.ToolsCount = src.Status.ToolsCount
	dst.Status.Tools = src.Status.Tools
	dst.Status.Endpoint = src.Status.Endpoint
	dst.Status.LastStartedAt = src.Status.LastStartedAt
	dst.Status.LastStoppedAt = src.Status.LastStoppedAt
	dst.Status.LastHealthCheck = src.Status.LastHealthCheck
	dst.Status.ConsecutiveFailures = src.Status.ConsecutiveFailures
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.PodName = src.Status.PodName

	// Status: conditions (custom -> metav1.Condition)
	dst.Status.Conditions = conditionsToStandard(src.Status.Conditions)

	// Status: capabilities
	if src.Status.Capabilities != nil {
		dst.Status.Capabilities = convertMCPServerCapabilitiesTo(src.Status.Capabilities)
	}

	// Status: violations
	dst.Status.Violations = convertViolationsTo(src.Status.Violations)

	return nil
}

// ConvertFrom converts the Hub version (v1alpha2) to this v1alpha1 MCPServer.
func (dst *MCPServer) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1alpha2.MCPServer)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPServer, got %T", srcRaw)
	}

	// ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Spec: direct fields
	dst.Spec.Mode = MCPServerMode(src.Spec.Mode)
	dst.Spec.Image = src.Spec.Image
	dst.Spec.Command = src.Spec.Command
	dst.Spec.Args = src.Spec.Args
	dst.Spec.WorkingDir = src.Spec.WorkingDir
	dst.Spec.Endpoint = src.Spec.Endpoint
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.ServiceAccountName = src.Spec.ServiceAccountName
	dst.Spec.ImagePullSecrets = src.Spec.ImagePullSecrets
	dst.Spec.NodeSelector = src.Spec.NodeSelector
	dst.Spec.Affinity = src.Spec.Affinity
	dst.Spec.PriorityClassName = src.Spec.PriorityClassName

	// Spec: duration fields (*metav1.Duration -> string)
	dst.Spec.IdleTTL = durationToString(src.Spec.IdleTTL)
	dst.Spec.StartupTimeout = durationToString(src.Spec.StartupTimeout)
	dst.Spec.ShutdownGracePeriod = durationToString(src.Spec.ShutdownGracePeriod)

	// Spec: nested structs
	if src.Spec.HealthCheck != nil {
		dst.Spec.HealthCheck = &HealthCheckConfig{
			Enabled:          src.Spec.HealthCheck.Enabled,
			Interval:         durationToString(src.Spec.HealthCheck.Interval),
			Timeout:          durationToString(src.Spec.HealthCheck.Timeout),
			FailureThreshold: src.Spec.HealthCheck.FailureThreshold,
			SuccessThreshold: src.Spec.HealthCheck.SuccessThreshold,
		}
	}

	if src.Spec.Resources != nil {
		dst.Spec.Resources = convertResourceRequirementsFrom(src.Spec.Resources)
	}

	dst.Spec.Env = convertEnvVarsFrom(src.Spec.Env)
	dst.Spec.Volumes = convertVolumesFrom(src.Spec.Volumes)

	if src.Spec.SecurityContext != nil {
		dst.Spec.SecurityContext = convertSecurityContextFrom(src.Spec.SecurityContext)
	}

	dst.Spec.Tolerations = convertTolerationsFrom(src.Spec.Tolerations)

	if src.Spec.Tools != nil {
		dst.Spec.Tools = convertToolsConfigFrom(src.Spec.Tools)
	}

	if src.Spec.CircuitBreaker != nil {
		dst.Spec.CircuitBreaker = &CircuitBreakerConfig{
			Enabled:          src.Spec.CircuitBreaker.Enabled,
			FailureThreshold: src.Spec.CircuitBreaker.FailureThreshold,
			SuccessThreshold: src.Spec.CircuitBreaker.SuccessThreshold,
			ResetTimeout:     durationToString(src.Spec.CircuitBreaker.ResetTimeout),
			HalfOpenRequests: src.Spec.CircuitBreaker.HalfOpenRequests,
		}
	}

	if src.Spec.Observability != nil {
		dst.Spec.Observability = convertObservabilityFrom(src.Spec.Observability)
	}

	if src.Spec.Capabilities != nil {
		dst.Spec.Capabilities = convertMCPServerCapabilitiesFrom(src.Spec.Capabilities)
	}

	// Status
	dst.Status.State = MCPServerState(src.Status.State)
	dst.Status.Phase = src.Status.Phase
	dst.Status.Replicas = src.Status.Replicas
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.AvailableReplicas = src.Status.AvailableReplicas
	dst.Status.ToolsCount = src.Status.ToolsCount
	dst.Status.Tools = src.Status.Tools
	dst.Status.Endpoint = src.Status.Endpoint
	dst.Status.LastStartedAt = src.Status.LastStartedAt
	dst.Status.LastStoppedAt = src.Status.LastStoppedAt
	dst.Status.LastHealthCheck = src.Status.LastHealthCheck
	dst.Status.ConsecutiveFailures = src.Status.ConsecutiveFailures
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.PodName = src.Status.PodName

	// Status: conditions (metav1.Condition -> custom)
	dst.Status.Conditions = conditionsFromStandard(src.Status.Conditions)

	// Status: capabilities
	if src.Status.Capabilities != nil {
		dst.Status.Capabilities = convertMCPServerCapabilitiesFrom(src.Status.Capabilities)
	}

	// Status: violations
	dst.Status.Violations = convertViolationsFrom(src.Status.Violations)

	return nil
}

// --- MCPServerGroup conversion ---

// Ensure MCPServerGroup implements conversion.Convertible
var _ conversion.Convertible = &MCPServerGroup{}

// ConvertTo converts this v1alpha1 MCPServerGroup to the Hub version (v1alpha2).
func (src *MCPServerGroup) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1alpha2.MCPServerGroup)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPServerGroup, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Selector = src.Spec.Selector
	dst.Spec.Strategy = v1alpha2.LoadBalancingStrategy(src.Spec.Strategy)

	var err error

	if src.Spec.Failover != nil {
		dst.Spec.Failover = &v1alpha2.FailoverConfig{
			Enabled:    src.Spec.Failover.Enabled,
			MaxRetries: src.Spec.Failover.MaxRetries,
			RetryOn:    src.Spec.Failover.RetryOn,
		}
		if dst.Spec.Failover.RetryDelay, err = stringToDuration(src.Spec.Failover.RetryDelay); err != nil {
			return fmt.Errorf("converting spec.failover.retryDelay: %w", err)
		}
	}

	if src.Spec.HealthPolicy != nil {
		dst.Spec.HealthPolicy = &v1alpha2.HealthPolicy{
			MinHealthyPercentage: src.Spec.HealthPolicy.MinHealthyPercentage,
			MinHealthyCount:      src.Spec.HealthPolicy.MinHealthyCount,
			UnhealthyThreshold:   src.Spec.HealthPolicy.UnhealthyThreshold,
		}
	}

	if src.Spec.SessionAffinity != nil {
		dst.Spec.SessionAffinity = &v1alpha2.SessionAffinityConfig{
			Enabled: src.Spec.SessionAffinity.Enabled,
			Type:    src.Spec.SessionAffinity.Type,
			Header:  src.Spec.SessionAffinity.Header,
		}
		if dst.Spec.SessionAffinity.TTL, err = stringToDuration(src.Spec.SessionAffinity.TTL); err != nil {
			return fmt.Errorf("converting spec.sessionAffinity.ttl: %w", err)
		}
	}

	if src.Spec.CircuitBreaker != nil {
		dst.Spec.CircuitBreaker = &v1alpha2.GroupCircuitBreakerConfig{
			Enabled:          src.Spec.CircuitBreaker.Enabled,
			FailureThreshold: src.Spec.CircuitBreaker.FailureThreshold,
		}
		if dst.Spec.CircuitBreaker.ResetTimeout, err = stringToDuration(src.Spec.CircuitBreaker.ResetTimeout); err != nil {
			return fmt.Errorf("converting spec.circuitBreaker.resetTimeout: %w", err)
		}
	}

	// Status
	dst.Status.ProviderCount = src.Status.ProviderCount
	dst.Status.ReadyCount = src.Status.ReadyCount
	dst.Status.DegradedCount = src.Status.DegradedCount
	dst.Status.ColdCount = src.Status.ColdCount
	dst.Status.DeadCount = src.Status.DeadCount
	dst.Status.ActiveStrategy = src.Status.ActiveStrategy
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = conditionsToStandard(src.Status.Conditions)

	for _, p := range src.Status.Providers {
		dst.Status.Providers = append(dst.Status.Providers, v1alpha2.MCPServerMemberStatus{
			Name:              p.Name,
			Namespace:         p.Namespace,
			State:             p.State,
			Weight:            p.Weight,
			ActiveConnections: p.ActiveConnections,
			LastHealthCheck:   p.LastHealthCheck,
		})
	}

	return nil
}

// ConvertFrom converts the Hub version (v1alpha2) to this v1alpha1 MCPServerGroup.
func (dst *MCPServerGroup) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1alpha2.MCPServerGroup)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPServerGroup, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Selector = src.Spec.Selector
	dst.Spec.Strategy = LoadBalancingStrategy(src.Spec.Strategy)

	if src.Spec.Failover != nil {
		dst.Spec.Failover = &FailoverConfig{
			Enabled:    src.Spec.Failover.Enabled,
			MaxRetries: src.Spec.Failover.MaxRetries,
			RetryDelay: durationToString(src.Spec.Failover.RetryDelay),
			RetryOn:    src.Spec.Failover.RetryOn,
		}
	}

	if src.Spec.HealthPolicy != nil {
		dst.Spec.HealthPolicy = &HealthPolicy{
			MinHealthyPercentage: src.Spec.HealthPolicy.MinHealthyPercentage,
			MinHealthyCount:      src.Spec.HealthPolicy.MinHealthyCount,
			UnhealthyThreshold:   src.Spec.HealthPolicy.UnhealthyThreshold,
		}
	}

	if src.Spec.SessionAffinity != nil {
		dst.Spec.SessionAffinity = &SessionAffinityConfig{
			Enabled: src.Spec.SessionAffinity.Enabled,
			Type:    src.Spec.SessionAffinity.Type,
			Header:  src.Spec.SessionAffinity.Header,
			TTL:     durationToString(src.Spec.SessionAffinity.TTL),
		}
	}

	if src.Spec.CircuitBreaker != nil {
		dst.Spec.CircuitBreaker = &GroupCircuitBreakerConfig{
			Enabled:          src.Spec.CircuitBreaker.Enabled,
			FailureThreshold: src.Spec.CircuitBreaker.FailureThreshold,
			ResetTimeout:     durationToString(src.Spec.CircuitBreaker.ResetTimeout),
		}
	}

	// Status
	dst.Status.ProviderCount = src.Status.ProviderCount
	dst.Status.ReadyCount = src.Status.ReadyCount
	dst.Status.DegradedCount = src.Status.DegradedCount
	dst.Status.ColdCount = src.Status.ColdCount
	dst.Status.DeadCount = src.Status.DeadCount
	dst.Status.ActiveStrategy = src.Status.ActiveStrategy
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = conditionsFromStandard(src.Status.Conditions)

	for _, p := range src.Status.Providers {
		dst.Status.Providers = append(dst.Status.Providers, MCPServerMemberStatus{
			Name:              p.Name,
			Namespace:         p.Namespace,
			State:             p.State,
			Weight:            p.Weight,
			ActiveConnections: p.ActiveConnections,
			LastHealthCheck:   p.LastHealthCheck,
		})
	}

	return nil
}

// --- MCPDiscoverySource conversion ---

// Ensure MCPDiscoverySource implements conversion.Convertible
var _ conversion.Convertible = &MCPDiscoverySource{}

// ConvertTo converts this v1alpha1 MCPDiscoverySource to the Hub version (v1alpha2).
func (src *MCPDiscoverySource) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1alpha2.MCPDiscoverySource)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPDiscoverySource, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Type = v1alpha2.DiscoveryType(src.Spec.Type)
	dst.Spec.Mode = v1alpha2.DiscoveryMode(src.Spec.Mode)
	dst.Spec.Paused = src.Spec.Paused

	var err error
	if dst.Spec.RefreshInterval, err = stringToDuration(src.Spec.RefreshInterval); err != nil {
		return fmt.Errorf("converting spec.refreshInterval: %w", err)
	}

	if src.Spec.NamespaceSelector != nil {
		dst.Spec.NamespaceSelector = &v1alpha2.NamespaceSelectorConfig{
			MatchLabels:       src.Spec.NamespaceSelector.MatchLabels,
			MatchExpressions:  src.Spec.NamespaceSelector.MatchExpressions,
			ExcludeNamespaces: src.Spec.NamespaceSelector.ExcludeNamespaces,
		}
	}

	if src.Spec.ConfigMapRef != nil {
		dst.Spec.ConfigMapRef = &v1alpha2.ConfigMapReference{
			Name:      src.Spec.ConfigMapRef.Name,
			Namespace: src.Spec.ConfigMapRef.Namespace,
			Key:       src.Spec.ConfigMapRef.Key,
		}
	}

	if src.Spec.Annotations != nil {
		dst.Spec.Annotations = &v1alpha2.AnnotationDiscoveryConfig{
			PodSelector:         src.Spec.Annotations.PodSelector,
			ServiceSelector:     src.Spec.Annotations.ServiceSelector,
			AnnotationPrefix:    src.Spec.Annotations.AnnotationPrefix,
			RequiredAnnotations: src.Spec.Annotations.RequiredAnnotations,
		}
	}

	if src.Spec.ServiceDiscovery != nil {
		dst.Spec.ServiceDiscovery = &v1alpha2.ServiceDiscoveryConfig{
			Selector: src.Spec.ServiceDiscovery.Selector,
			PortName: src.Spec.ServiceDiscovery.PortName,
			Protocol: src.Spec.ServiceDiscovery.Protocol,
		}
	}

	if src.Spec.MCPServerTemplate != nil {
		dst.Spec.MCPServerTemplate = &v1alpha2.MCPServerTemplateConfig{}
		if src.Spec.MCPServerTemplate.Metadata != nil {
			dst.Spec.MCPServerTemplate.Metadata = &v1alpha2.TemplateMetadata{
				Labels:      src.Spec.MCPServerTemplate.Metadata.Labels,
				Annotations: src.Spec.MCPServerTemplate.Metadata.Annotations,
			}
		}
		if src.Spec.MCPServerTemplate.Spec != nil {
			// Convert the embedded MCPServerSpec via a temporary MCPServer round-trip.
			// This reuses the provider conversion logic for all the duration and nested fields.
			tmpSrc := &MCPServer{Spec: *src.Spec.MCPServerTemplate.Spec}
			tmpDst := &v1alpha2.MCPServer{}
			if err := tmpSrc.ConvertTo(tmpDst); err != nil {
				return fmt.Errorf("converting spec.providerTemplate.spec: %w", err)
			}
			dst.Spec.MCPServerTemplate.Spec = &tmpDst.Spec
		}
	}

	if src.Spec.Filters != nil {
		dst.Spec.Filters = &v1alpha2.DiscoveryFilters{
			IncludePatterns: src.Spec.Filters.IncludePatterns,
			ExcludePatterns: src.Spec.Filters.ExcludePatterns,
			MaxProviders:    src.Spec.Filters.MaxProviders,
		}
	}

	if src.Spec.Ownership != nil {
		dst.Spec.Ownership = &v1alpha2.OwnershipConfig{
			Controller:    src.Spec.Ownership.Controller,
			BlockDeletion: src.Spec.Ownership.BlockDeletion,
		}
	}

	// Status
	dst.Status.DiscoveredCount = src.Status.DiscoveredCount
	dst.Status.ManagedCount = src.Status.ManagedCount
	dst.Status.LastSyncTime = src.Status.LastSyncTime
	dst.Status.LastSyncError = src.Status.LastSyncError
	dst.Status.NextSyncTime = src.Status.NextSyncTime
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration

	if dst.Status.LastSyncDuration, err = stringToDuration(src.Status.LastSyncDuration); err != nil {
		return fmt.Errorf("converting status.lastSyncDuration: %w", err)
	}

	dst.Status.Conditions = conditionsToStandard(src.Status.Conditions)

	for _, p := range src.Status.DiscoveredMCPServers {
		dst.Status.DiscoveredMCPServers = append(dst.Status.DiscoveredMCPServers, v1alpha2.DiscoveredMCPServer{
			Name:         p.Name,
			Source:       p.Source,
			DiscoveredAt: p.DiscoveredAt,
			Managed:      p.Managed,
			Error:        p.Error,
		})
	}

	return nil
}

// ConvertFrom converts the Hub version (v1alpha2) to this v1alpha1 MCPDiscoverySource.
func (dst *MCPDiscoverySource) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1alpha2.MCPDiscoverySource)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.MCPDiscoverySource, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Type = DiscoveryType(src.Spec.Type)
	dst.Spec.Mode = DiscoveryMode(src.Spec.Mode)
	dst.Spec.RefreshInterval = durationToString(src.Spec.RefreshInterval)
	dst.Spec.Paused = src.Spec.Paused

	if src.Spec.NamespaceSelector != nil {
		dst.Spec.NamespaceSelector = &NamespaceSelectorConfig{
			MatchLabels:       src.Spec.NamespaceSelector.MatchLabels,
			MatchExpressions:  src.Spec.NamespaceSelector.MatchExpressions,
			ExcludeNamespaces: src.Spec.NamespaceSelector.ExcludeNamespaces,
		}
	}

	if src.Spec.ConfigMapRef != nil {
		dst.Spec.ConfigMapRef = &ConfigMapReference{
			Name:      src.Spec.ConfigMapRef.Name,
			Namespace: src.Spec.ConfigMapRef.Namespace,
			Key:       src.Spec.ConfigMapRef.Key,
		}
	}

	if src.Spec.Annotations != nil {
		dst.Spec.Annotations = &AnnotationDiscoveryConfig{
			PodSelector:         src.Spec.Annotations.PodSelector,
			ServiceSelector:     src.Spec.Annotations.ServiceSelector,
			AnnotationPrefix:    src.Spec.Annotations.AnnotationPrefix,
			RequiredAnnotations: src.Spec.Annotations.RequiredAnnotations,
		}
	}

	if src.Spec.ServiceDiscovery != nil {
		dst.Spec.ServiceDiscovery = &ServiceDiscoveryConfig{
			Selector: src.Spec.ServiceDiscovery.Selector,
			PortName: src.Spec.ServiceDiscovery.PortName,
			Protocol: src.Spec.ServiceDiscovery.Protocol,
		}
	}

	if src.Spec.MCPServerTemplate != nil {
		dst.Spec.MCPServerTemplate = &MCPServerTemplateConfig{}
		if src.Spec.MCPServerTemplate.Metadata != nil {
			dst.Spec.MCPServerTemplate.Metadata = &TemplateMetadata{
				Labels:      src.Spec.MCPServerTemplate.Metadata.Labels,
				Annotations: src.Spec.MCPServerTemplate.Metadata.Annotations,
			}
		}
		if src.Spec.MCPServerTemplate.Spec != nil {
			tmpSrc := &v1alpha2.MCPServer{Spec: *src.Spec.MCPServerTemplate.Spec}
			tmpDst := &MCPServer{}
			if err := tmpDst.ConvertFrom(tmpSrc); err != nil {
				return fmt.Errorf("converting spec.providerTemplate.spec: %w", err)
			}
			dst.Spec.MCPServerTemplate.Spec = &tmpDst.Spec
		}
	}

	if src.Spec.Filters != nil {
		dst.Spec.Filters = &DiscoveryFilters{
			IncludePatterns: src.Spec.Filters.IncludePatterns,
			ExcludePatterns: src.Spec.Filters.ExcludePatterns,
			MaxProviders:    src.Spec.Filters.MaxProviders,
		}
	}

	if src.Spec.Ownership != nil {
		dst.Spec.Ownership = &OwnershipConfig{
			Controller:    src.Spec.Ownership.Controller,
			BlockDeletion: src.Spec.Ownership.BlockDeletion,
		}
	}

	// Status
	dst.Status.DiscoveredCount = src.Status.DiscoveredCount
	dst.Status.ManagedCount = src.Status.ManagedCount
	dst.Status.LastSyncTime = src.Status.LastSyncTime
	dst.Status.LastSyncDuration = durationToString(src.Status.LastSyncDuration)
	dst.Status.LastSyncError = src.Status.LastSyncError
	dst.Status.NextSyncTime = src.Status.NextSyncTime
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration

	dst.Status.Conditions = conditionsFromStandard(src.Status.Conditions)

	for _, p := range src.Status.DiscoveredMCPServers {
		dst.Status.DiscoveredMCPServers = append(dst.Status.DiscoveredMCPServers, DiscoveredMCPServer{
			Name:         p.Name,
			Source:       p.Source,
			DiscoveredAt: p.DiscoveredAt,
			Managed:      p.Managed,
			Error:        p.Error,
		})
	}

	return nil
}

// --- Shared conversion helpers ---

// stringToDuration converts a Go duration string (e.g. "5m") to *metav1.Duration.
// Returns nil for empty strings.
func stringToDuration(s string) (*metav1.Duration, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return &metav1.Duration{Duration: d}, nil
}

// durationToString converts *metav1.Duration to a Go duration string.
// Returns empty string for nil.
func durationToString(d *metav1.Duration) string {
	if d == nil {
		return ""
	}
	return d.Duration.String()
}

// conditionsToStandard converts custom v1alpha1 Conditions to standard metav1.Condition.
func conditionsToStandard(src []Condition) []metav1.Condition {
	if len(src) == 0 {
		return nil
	}
	dst := make([]metav1.Condition, len(src))
	for i, c := range src {
		dst[i] = metav1.Condition{
			Type:               c.Type,
			Status:             c.Status,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		}
	}
	return dst
}

// conditionsFromStandard converts standard metav1.Condition to custom v1alpha1 Conditions.
func conditionsFromStandard(src []metav1.Condition) []Condition {
	if len(src) == 0 {
		return nil
	}
	dst := make([]Condition, len(src))
	for i, c := range src {
		dst[i] = Condition{
			Type:               c.Type,
			Status:             c.Status,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		}
	}
	return dst
}

// --- Type conversion helpers (v1alpha1 -> v1alpha2) ---

func convertResourceRequirementsTo(src *ResourceRequirements) *v1alpha2.ResourceRequirements {
	dst := &v1alpha2.ResourceRequirements{}
	if src.Requests != nil {
		dst.Requests = &v1alpha2.ResourceList{CPU: src.Requests.CPU, Memory: src.Requests.Memory}
	}
	if src.Limits != nil {
		dst.Limits = &v1alpha2.ResourceList{CPU: src.Limits.CPU, Memory: src.Limits.Memory}
	}
	return dst
}

func convertResourceRequirementsFrom(src *v1alpha2.ResourceRequirements) *ResourceRequirements {
	dst := &ResourceRequirements{}
	if src.Requests != nil {
		dst.Requests = &ResourceList{CPU: src.Requests.CPU, Memory: src.Requests.Memory}
	}
	if src.Limits != nil {
		dst.Limits = &ResourceList{CPU: src.Limits.CPU, Memory: src.Limits.Memory}
	}
	return dst
}

func convertEnvVarsTo(src []EnvVar) []v1alpha2.EnvVar {
	if len(src) == 0 {
		return nil
	}
	dst := make([]v1alpha2.EnvVar, len(src))
	for i, e := range src {
		dst[i] = v1alpha2.EnvVar{Name: e.Name, Value: e.Value}
		if e.ValueFrom != nil {
			dst[i].ValueFrom = &v1alpha2.EnvVarSource{}
			if e.ValueFrom.SecretKeyRef != nil {
				dst[i].ValueFrom.SecretKeyRef = &v1alpha2.SecretKeySelector{
					Name: e.ValueFrom.SecretKeyRef.Name, Key: e.ValueFrom.SecretKeyRef.Key, Optional: e.ValueFrom.SecretKeyRef.Optional,
				}
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				dst[i].ValueFrom.ConfigMapKeyRef = &v1alpha2.ConfigMapKeySelector{
					Name: e.ValueFrom.ConfigMapKeyRef.Name, Key: e.ValueFrom.ConfigMapKeyRef.Key, Optional: e.ValueFrom.ConfigMapKeyRef.Optional,
				}
			}
		}
	}
	return dst
}

func convertEnvVarsFrom(src []v1alpha2.EnvVar) []EnvVar {
	if len(src) == 0 {
		return nil
	}
	dst := make([]EnvVar, len(src))
	for i, e := range src {
		dst[i] = EnvVar{Name: e.Name, Value: e.Value}
		if e.ValueFrom != nil {
			dst[i].ValueFrom = &EnvVarSource{}
			if e.ValueFrom.SecretKeyRef != nil {
				dst[i].ValueFrom.SecretKeyRef = &SecretKeySelector{
					Name: e.ValueFrom.SecretKeyRef.Name, Key: e.ValueFrom.SecretKeyRef.Key, Optional: e.ValueFrom.SecretKeyRef.Optional,
				}
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				dst[i].ValueFrom.ConfigMapKeyRef = &ConfigMapKeySelector{
					Name: e.ValueFrom.ConfigMapKeyRef.Name, Key: e.ValueFrom.ConfigMapKeyRef.Key, Optional: e.ValueFrom.ConfigMapKeyRef.Optional,
				}
			}
		}
	}
	return dst
}

func convertVolumesTo(src []Volume) []v1alpha2.Volume {
	if len(src) == 0 {
		return nil
	}
	dst := make([]v1alpha2.Volume, len(src))
	for i, v := range src {
		dst[i] = v1alpha2.Volume{
			Name: v.Name, MountPath: v.MountPath, SubPath: v.SubPath, ReadOnly: v.ReadOnly,
		}
		if v.Secret != nil {
			dst[i].Secret = &v1alpha2.SecretVolumeSource{SecretName: v.Secret.SecretName}
			for _, item := range v.Secret.Items {
				dst[i].Secret.Items = append(dst[i].Secret.Items, v1alpha2.KeyToPath{Key: item.Key, Path: item.Path})
			}
		}
		if v.ConfigMap != nil {
			dst[i].ConfigMap = &v1alpha2.ConfigMapVolumeSource{Name: v.ConfigMap.Name}
			for _, item := range v.ConfigMap.Items {
				dst[i].ConfigMap.Items = append(dst[i].ConfigMap.Items, v1alpha2.KeyToPath{Key: item.Key, Path: item.Path})
			}
		}
		if v.PersistentVolumeClaim != nil {
			dst[i].PersistentVolumeClaim = &v1alpha2.PVCVolumeSource{ClaimName: v.PersistentVolumeClaim.ClaimName}
		}
		if v.EmptyDir != nil {
			dst[i].EmptyDir = &v1alpha2.EmptyDirVolumeSource{Medium: v.EmptyDir.Medium, SizeLimit: v.EmptyDir.SizeLimit}
		}
	}
	return dst
}

func convertVolumesFrom(src []v1alpha2.Volume) []Volume {
	if len(src) == 0 {
		return nil
	}
	dst := make([]Volume, len(src))
	for i, v := range src {
		dst[i] = Volume{
			Name: v.Name, MountPath: v.MountPath, SubPath: v.SubPath, ReadOnly: v.ReadOnly,
		}
		if v.Secret != nil {
			dst[i].Secret = &SecretVolumeSource{SecretName: v.Secret.SecretName}
			for _, item := range v.Secret.Items {
				dst[i].Secret.Items = append(dst[i].Secret.Items, KeyToPath{Key: item.Key, Path: item.Path})
			}
		}
		if v.ConfigMap != nil {
			dst[i].ConfigMap = &ConfigMapVolumeSource{Name: v.ConfigMap.Name}
			for _, item := range v.ConfigMap.Items {
				dst[i].ConfigMap.Items = append(dst[i].ConfigMap.Items, KeyToPath{Key: item.Key, Path: item.Path})
			}
		}
		if v.PersistentVolumeClaim != nil {
			dst[i].PersistentVolumeClaim = &PVCVolumeSource{ClaimName: v.PersistentVolumeClaim.ClaimName}
		}
		if v.EmptyDir != nil {
			dst[i].EmptyDir = &EmptyDirVolumeSource{Medium: v.EmptyDir.Medium, SizeLimit: v.EmptyDir.SizeLimit}
		}
	}
	return dst
}

func convertSecurityContextTo(src *SecurityContext) *v1alpha2.SecurityContext {
	dst := &v1alpha2.SecurityContext{
		RunAsNonRoot:             src.RunAsNonRoot,
		RunAsUser:                src.RunAsUser,
		RunAsGroup:               src.RunAsGroup,
		FSGroup:                  src.FSGroup,
		ReadOnlyRootFilesystem:   src.ReadOnlyRootFilesystem,
		AllowPrivilegeEscalation: src.AllowPrivilegeEscalation,
	}
	if src.Capabilities != nil {
		dst.Capabilities = &v1alpha2.Capabilities{Add: src.Capabilities.Add, Drop: src.Capabilities.Drop}
	}
	if src.SeccompProfile != nil {
		dst.SeccompProfile = &v1alpha2.SeccompProfile{Type: src.SeccompProfile.Type}
	}
	return dst
}

func convertSecurityContextFrom(src *v1alpha2.SecurityContext) *SecurityContext {
	dst := &SecurityContext{
		RunAsNonRoot:             src.RunAsNonRoot,
		RunAsUser:                src.RunAsUser,
		RunAsGroup:               src.RunAsGroup,
		FSGroup:                  src.FSGroup,
		ReadOnlyRootFilesystem:   src.ReadOnlyRootFilesystem,
		AllowPrivilegeEscalation: src.AllowPrivilegeEscalation,
	}
	if src.Capabilities != nil {
		dst.Capabilities = &Capabilities{Add: src.Capabilities.Add, Drop: src.Capabilities.Drop}
	}
	if src.SeccompProfile != nil {
		dst.SeccompProfile = &SeccompProfile{Type: src.SeccompProfile.Type}
	}
	return dst
}

func convertTolerationsTo(src []Toleration) []v1alpha2.Toleration {
	if len(src) == 0 {
		return nil
	}
	dst := make([]v1alpha2.Toleration, len(src))
	for i, t := range src {
		dst[i] = v1alpha2.Toleration{
			Key: t.Key, Operator: t.Operator, Value: t.Value, Effect: t.Effect, TolerationSeconds: t.TolerationSeconds,
		}
	}
	return dst
}

func convertTolerationsFrom(src []v1alpha2.Toleration) []Toleration {
	if len(src) == 0 {
		return nil
	}
	dst := make([]Toleration, len(src))
	for i, t := range src {
		dst[i] = Toleration{
			Key: t.Key, Operator: t.Operator, Value: t.Value, Effect: t.Effect, TolerationSeconds: t.TolerationSeconds,
		}
	}
	return dst
}

func convertToolsConfigTo(src *ToolsConfig) *v1alpha2.ToolsConfig {
	dst := &v1alpha2.ToolsConfig{
		AllowList: src.AllowList,
		DenyList:  src.DenyList,
	}
	if src.RateLimit != nil {
		dst.RateLimit = &v1alpha2.RateLimitConfig{
			RequestsPerMinute: src.RateLimit.RequestsPerMinute,
			BurstSize:         src.RateLimit.BurstSize,
		}
	}
	return dst
}

func convertToolsConfigFrom(src *v1alpha2.ToolsConfig) *ToolsConfig {
	dst := &ToolsConfig{
		AllowList: src.AllowList,
		DenyList:  src.DenyList,
	}
	if src.RateLimit != nil {
		dst.RateLimit = &RateLimitConfig{
			RequestsPerMinute: src.RateLimit.RequestsPerMinute,
			BurstSize:         src.RateLimit.BurstSize,
		}
	}
	return dst
}

func convertObservabilityTo(src *ObservabilityConfig) *v1alpha2.ObservabilityConfig {
	dst := &v1alpha2.ObservabilityConfig{}
	if src.Tracing != nil {
		dst.Tracing = &v1alpha2.TracingConfig{Enabled: src.Tracing.Enabled, SamplingRate: src.Tracing.SamplingRate}
	}
	if src.Metrics != nil {
		dst.Metrics = &v1alpha2.MetricsConfig{Enabled: src.Metrics.Enabled, Port: src.Metrics.Port}
	}
	return dst
}

func convertObservabilityFrom(src *v1alpha2.ObservabilityConfig) *ObservabilityConfig {
	dst := &ObservabilityConfig{}
	if src.Tracing != nil {
		dst.Tracing = &TracingConfig{Enabled: src.Tracing.Enabled, SamplingRate: src.Tracing.SamplingRate}
	}
	if src.Metrics != nil {
		dst.Metrics = &MetricsConfig{Enabled: src.Metrics.Enabled, Port: src.Metrics.Port}
	}
	return dst
}

func convertMCPServerCapabilitiesTo(src *MCPServerCapabilities) *v1alpha2.MCPServerCapabilities {
	dst := &v1alpha2.MCPServerCapabilities{
		EnforcementMode: src.EnforcementMode,
	}
	if src.Network != nil {
		dst.Network = &v1alpha2.NetworkCapabilitiesSpec{
			DNSAllowed:      src.Network.DNSAllowed,
			LoopbackAllowed: src.Network.LoopbackAllowed,
		}
		for _, e := range src.Network.Egress {
			dst.Network.Egress = append(dst.Network.Egress, v1alpha2.EgressRuleSpec{
				Host: e.Host, Port: e.Port, Protocol: e.Protocol, CIDR: e.CIDR,
			})
		}
	}
	if src.Filesystem != nil {
		dst.Filesystem = &v1alpha2.FilesystemCapabilitiesSpec{
			ReadPaths: src.Filesystem.ReadPaths, WritePaths: src.Filesystem.WritePaths, TempAllowed: src.Filesystem.TempAllowed,
		}
	}
	if src.Environment != nil {
		dst.Environment = &v1alpha2.EnvironmentCapabilitiesSpec{
			Required: src.Environment.Required, Optional: src.Environment.Optional,
		}
	}
	if src.Tools != nil {
		dst.Tools = &v1alpha2.ToolCapabilitiesSpec{
			MaxCount: src.Tools.MaxCount, SchemaDriftAlert: src.Tools.SchemaDriftAlert, ExpectedTools: src.Tools.ExpectedTools,
		}
	}
	if src.Resources != nil {
		dst.Resources = &v1alpha2.ResourceCapabilitiesSpec{
			MaxMemoryMB: src.Resources.MaxMemoryMB, MaxCPUPercent: src.Resources.MaxCPUPercent,
		}
	}
	return dst
}

func convertMCPServerCapabilitiesFrom(src *v1alpha2.MCPServerCapabilities) *MCPServerCapabilities {
	dst := &MCPServerCapabilities{
		EnforcementMode: src.EnforcementMode,
	}
	if src.Network != nil {
		dst.Network = &NetworkCapabilitiesSpec{
			DNSAllowed:      src.Network.DNSAllowed,
			LoopbackAllowed: src.Network.LoopbackAllowed,
		}
		for _, e := range src.Network.Egress {
			dst.Network.Egress = append(dst.Network.Egress, EgressRuleSpec{
				Host: e.Host, Port: e.Port, Protocol: e.Protocol, CIDR: e.CIDR,
			})
		}
	}
	if src.Filesystem != nil {
		dst.Filesystem = &FilesystemCapabilitiesSpec{
			ReadPaths: src.Filesystem.ReadPaths, WritePaths: src.Filesystem.WritePaths, TempAllowed: src.Filesystem.TempAllowed,
		}
	}
	if src.Environment != nil {
		dst.Environment = &EnvironmentCapabilitiesSpec{
			Required: src.Environment.Required, Optional: src.Environment.Optional,
		}
	}
	if src.Tools != nil {
		dst.Tools = &ToolCapabilitiesSpec{
			MaxCount: src.Tools.MaxCount, SchemaDriftAlert: src.Tools.SchemaDriftAlert, ExpectedTools: src.Tools.ExpectedTools,
		}
	}
	if src.Resources != nil {
		dst.Resources = &ResourceCapabilitiesSpec{
			MaxMemoryMB: src.Resources.MaxMemoryMB, MaxCPUPercent: src.Resources.MaxCPUPercent,
		}
	}
	return dst
}

func convertViolationsTo(src []ViolationRecord) []v1alpha2.ViolationRecord {
	if len(src) == 0 {
		return nil
	}
	dst := make([]v1alpha2.ViolationRecord, len(src))
	for i, v := range src {
		dst[i] = v1alpha2.ViolationRecord{
			Type: v.Type, Detail: v.Detail, Severity: v.Severity,
			Action: v.Action, Destination: v.Destination, Timestamp: v.Timestamp,
		}
	}
	return dst
}

func convertViolationsFrom(src []v1alpha2.ViolationRecord) []ViolationRecord {
	if len(src) == 0 {
		return nil
	}
	dst := make([]ViolationRecord, len(src))
	for i, v := range src {
		dst[i] = ViolationRecord{
			Type: v.Type, Detail: v.Detail, Severity: v.Severity,
			Action: v.Action, Destination: v.Destination, Timestamp: v.Timestamp,
		}
	}
	return dst
}

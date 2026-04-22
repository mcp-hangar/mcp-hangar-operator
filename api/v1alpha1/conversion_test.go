package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// --- Helper constructors ---

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }
func int64Ptr(i int64) *int64 { return &i }

func durationPtr(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

// --- MCPServer round-trip tests ---

func TestMCPServer_ConvertTo_MinimalSpec(t *testing.T) {
	src := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-provider",
			Namespace: "default",
		},
		Spec: MCPServerSpec{
			Mode:  MCPServerModeContainer,
			Image: "example/provider:latest",
		},
	}

	dst := &v1alpha2.MCPServer{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Equal(t, "test-provider", dst.Name)
	assert.Equal(t, v1alpha2.MCPServerModeContainer, dst.Spec.Mode)
	assert.Equal(t, "example/provider:latest", dst.Spec.Image)
	assert.Nil(t, dst.Spec.IdleTTL)
}

func TestMCPServer_RoundTrip_FullSpec(t *testing.T) {
	original := &MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "full-provider",
			Namespace: "mcp-system",
			Labels:    map[string]string{"app": "mcp"},
		},
		Spec: MCPServerSpec{
			Mode:                MCPServerModeContainer,
			Image:               "ghcr.io/example/math-mcp:v1.2.3",
			Command:             []string{"/bin/server"},
			Args:                []string{"--port=8080"},
			WorkingDir:          "/app",
			Replicas:            int32Ptr(3),
			IdleTTL:             "5m0s",
			StartupTimeout:      "30s",
			ShutdownGracePeriod: "15s",
			ServiceAccountName:  "mcp-sa",
			PriorityClassName:   "high-priority",
			ImagePullSecrets:    []corev1.LocalObjectReference{{Name: "registry-secret"}},
			NodeSelector:        map[string]string{"node-type": "gpu"},
			HealthCheck: &HealthCheckConfig{
				Enabled:          boolPtr(true),
				Interval:         "1m0s",
				Timeout:          "10s",
				FailureThreshold: 5,
				SuccessThreshold: 2,
			},
			Resources: &ResourceRequirements{
				Requests: &ResourceList{CPU: "100m", Memory: "128Mi"},
				Limits:   &ResourceList{CPU: "500m", Memory: "512Mi"},
			},
			Env: []EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "SECRET_KEY", ValueFrom: &EnvVarSource{
					SecretKeyRef: &SecretKeySelector{Name: "my-secret", Key: "api-key", Optional: boolPtr(false)},
				}},
				{Name: "CONFIG_VAL", ValueFrom: &EnvVarSource{
					ConfigMapKeyRef: &ConfigMapKeySelector{Name: "my-config", Key: "val", Optional: boolPtr(true)},
				}},
			},
			Volumes: []Volume{
				{
					Name: "data", MountPath: "/data", ReadOnly: true,
					Secret: &SecretVolumeSource{SecretName: "data-secret", Items: []KeyToPath{{Key: "file", Path: "file.txt"}}},
				},
				{
					Name: "config", MountPath: "/config", SubPath: "sub",
					ConfigMap: &ConfigMapVolumeSource{Name: "config-map", Items: []KeyToPath{{Key: "k", Path: "v"}}},
				},
				{
					Name: "pvc-vol", MountPath: "/pvc",
					PersistentVolumeClaim: &PVCVolumeSource{ClaimName: "my-pvc"},
				},
				{
					Name: "temp", MountPath: "/tmp",
					EmptyDir: &EmptyDirVolumeSource{Medium: "Memory", SizeLimit: "1Gi"},
				},
			},
			SecurityContext: &SecurityContext{
				RunAsNonRoot:             boolPtr(true),
				RunAsUser:                int64Ptr(1000),
				RunAsGroup:               int64Ptr(1000),
				FSGroup:                  int64Ptr(2000),
				ReadOnlyRootFilesystem:   boolPtr(true),
				AllowPrivilegeEscalation: boolPtr(false),
				Capabilities:             &Capabilities{Add: []string{"NET_BIND_SERVICE"}, Drop: []string{"ALL"}},
				SeccompProfile:           &SeccompProfile{Type: "RuntimeDefault"},
			},
			Tolerations: []Toleration{
				{Key: "dedicated", Operator: "Equal", Value: "mcp", Effect: "NoSchedule", TolerationSeconds: int64Ptr(300)},
			},
			Tools: &ToolsConfig{
				AllowList: []string{"calculate", "format"},
				RateLimit: &RateLimitConfig{RequestsPerMinute: 60, BurstSize: 10},
			},
			CircuitBreaker: &CircuitBreakerConfig{
				Enabled:          boolPtr(true),
				FailureThreshold: 5,
				SuccessThreshold: 2,
				ResetTimeout:     "45s",
				HalfOpenRequests: 3,
			},
			Observability: &ObservabilityConfig{
				Tracing: &TracingConfig{Enabled: true, SamplingRate: "0.1"},
				Metrics: &MetricsConfig{Enabled: true, Port: 9090},
			},
			Capabilities: &MCPServerCapabilities{
				EnforcementMode: "block",
				Network: &NetworkCapabilitiesSpec{
					Egress:          []EgressRuleSpec{{Host: "api.example.com", Port: 443, Protocol: "https"}},
					DNSAllowed:      boolPtr(true),
					LoopbackAllowed: boolPtr(false),
				},
				Filesystem: &FilesystemCapabilitiesSpec{
					ReadPaths: []string{"/data"}, WritePaths: []string{"/tmp"}, TempAllowed: boolPtr(true),
				},
				Environment: &EnvironmentCapabilitiesSpec{
					Required: []string{"API_KEY"}, Optional: []string{"DEBUG"},
				},
				Tools: &ToolCapabilitiesSpec{
					MaxCount: 10, SchemaDriftAlert: boolPtr(true), ExpectedTools: []string{"calculate"},
				},
				Resources: &ResourceCapabilitiesSpec{
					MaxMemoryMB: 512, MaxCPUPercent: "80",
				},
			},
		},
		Status: MCPServerStatus{
			State:               MCPServerStateReady,
			Phase:               "Running",
			Replicas:            3,
			ReadyReplicas:       3,
			AvailableReplicas:   3,
			ToolsCount:          2,
			Tools:               []string{"calculate", "format"},
			Endpoint:            "http://10.0.0.1:8080",
			LastStartedAt:       &metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			LastStoppedAt:       &metav1.Time{Time: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)},
			LastHealthCheck:     &metav1.Time{Time: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)},
			ConsecutiveFailures: 0,
			ObservedGeneration:  5,
			PodName:             "mcp-provider-full-provider",
			Conditions: []Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
					Reason:             "ProviderReady",
					Message:            "Provider is ready",
					ObservedGeneration: 5,
				},
			},
			Violations: []ViolationRecord{
				{
					Type:        "egress_denied",
					Detail:      "connection to blocked host",
					Severity:    "high",
					Action:      "block",
					Destination: "evil.com:443",
					Timestamp:   metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)},
				},
			},
		},
	}

	// Convert v1alpha1 -> v1alpha2
	hub := &v1alpha2.MCPServer{}
	require.NoError(t, original.ConvertTo(hub))

	// Verify key v1alpha2 improvements
	assert.Equal(t, durationPtr(5*time.Minute), hub.Spec.IdleTTL)
	assert.Equal(t, durationPtr(30*time.Second), hub.Spec.StartupTimeout)
	assert.Equal(t, durationPtr(15*time.Second), hub.Spec.ShutdownGracePeriod)
	assert.Equal(t, durationPtr(1*time.Minute), hub.Spec.HealthCheck.Interval)
	assert.Equal(t, durationPtr(10*time.Second), hub.Spec.HealthCheck.Timeout)
	assert.Equal(t, durationPtr(45*time.Second), hub.Spec.CircuitBreaker.ResetTimeout)
	// Conditions should be metav1.Condition
	require.Len(t, hub.Status.Conditions, 1)
	assert.Equal(t, "Ready", hub.Status.Conditions[0].Type)
	assert.Equal(t, int64(5), hub.Status.Conditions[0].ObservedGeneration)

	// Convert v1alpha2 -> v1alpha1 (round-trip)
	roundTripped := &MCPServer{}
	require.NoError(t, roundTripped.ConvertFrom(hub))

	// Verify round-trip fidelity
	assert.Equal(t, original.Name, roundTripped.Name)
	assert.Equal(t, original.Namespace, roundTripped.Namespace)
	assert.Equal(t, original.Labels, roundTripped.Labels)
	assert.Equal(t, original.Spec, roundTripped.Spec)
	assert.Equal(t, original.Status.State, roundTripped.Status.State)
	assert.Equal(t, original.Status.Phase, roundTripped.Status.Phase)
	assert.Equal(t, original.Status.Replicas, roundTripped.Status.Replicas)
	assert.Equal(t, original.Status.Tools, roundTripped.Status.Tools)
	assert.Equal(t, original.Status.PodName, roundTripped.Status.PodName)
	assert.Equal(t, original.Status.Violations, roundTripped.Status.Violations)

	// Conditions round-trip
	require.Len(t, roundTripped.Status.Conditions, 1)
	assert.Equal(t, original.Status.Conditions[0].Type, roundTripped.Status.Conditions[0].Type)
	assert.Equal(t, original.Status.Conditions[0].Status, roundTripped.Status.Conditions[0].Status)
	assert.Equal(t, original.Status.Conditions[0].Reason, roundTripped.Status.Conditions[0].Reason)
	assert.Equal(t, original.Status.Conditions[0].Message, roundTripped.Status.Conditions[0].Message)
	assert.Equal(t, original.Status.Conditions[0].ObservedGeneration, roundTripped.Status.Conditions[0].ObservedGeneration)
}

func TestMCPServer_ConvertTo_InvalidDuration(t *testing.T) {
	src := &MCPServer{
		Spec: MCPServerSpec{
			Mode:    MCPServerModeContainer,
			Image:   "test:latest",
			IdleTTL: "not-a-duration",
		},
	}

	dst := &v1alpha2.MCPServer{}
	err := src.ConvertTo(dst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "spec.idleTTL")
}

func TestMCPServer_ConvertTo_EmptyDurations(t *testing.T) {
	src := &MCPServer{
		Spec: MCPServerSpec{
			Mode:     MCPServerModeRemote,
			Endpoint: "http://example.com",
		},
	}

	dst := &v1alpha2.MCPServer{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Nil(t, dst.Spec.IdleTTL)
	assert.Nil(t, dst.Spec.StartupTimeout)
	assert.Nil(t, dst.Spec.ShutdownGracePeriod)
}

func TestMCPServer_ConvertTo_WrongType(t *testing.T) {
	src := &MCPServer{}
	err := src.ConvertTo(&v1alpha2.MCPServerGroup{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.MCPServer")
}

func TestMCPServer_ConvertFrom_WrongType(t *testing.T) {
	dst := &MCPServer{}
	err := dst.ConvertFrom(&v1alpha2.MCPServerGroup{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.MCPServer")
}

// --- MCPServerGroup round-trip tests ---

func TestMCPServerGroup_RoundTrip(t *testing.T) {
	original := &MCPServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Spec: MCPServerGroupSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "backend"},
			},
			Strategy: StrategyWeighted,
			Failover: &FailoverConfig{
				Enabled:    boolPtr(true),
				MaxRetries: 3,
				RetryDelay: "2s",
				RetryOn:    []string{"timeout"},
			},
			HealthPolicy: &HealthPolicy{
				MinHealthyPercentage: 75,
				MinHealthyCount:      int32Ptr(2),
				UnhealthyThreshold:   5,
			},
			SessionAffinity: &SessionAffinityConfig{
				Enabled: true,
				Type:    "Header",
				Header:  "X-Session-ID",
				TTL:     "15m0s",
			},
			CircuitBreaker: &GroupCircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 10,
				ResetTimeout:     "2m0s",
			},
		},
		Status: MCPServerGroupStatus{
			ProviderCount:      5,
			ReadyCount:         4,
			DegradedCount:      1,
			ColdCount:          0,
			DeadCount:          0,
			ActiveStrategy:     "Weighted",
			ObservedGeneration: 3,
			Conditions: []Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Healthy", Message: "Group is healthy"},
			},
			Providers: []MCPServerMemberStatus{
				{Name: "p1", Namespace: "default", State: "Ready", Weight: 10, ActiveConnections: 5},
			},
		},
	}

	hub := &v1alpha2.MCPServerGroup{}
	require.NoError(t, original.ConvertTo(hub))

	// Verify duration conversions
	assert.Equal(t, durationPtr(2*time.Second), hub.Spec.Failover.RetryDelay)
	assert.Equal(t, durationPtr(15*time.Minute), hub.Spec.SessionAffinity.TTL)
	assert.Equal(t, durationPtr(2*time.Minute), hub.Spec.CircuitBreaker.ResetTimeout)

	roundTripped := &MCPServerGroup{}
	require.NoError(t, roundTripped.ConvertFrom(hub))

	assert.Equal(t, original.Spec.Selector, roundTripped.Spec.Selector)
	assert.Equal(t, original.Spec.Strategy, roundTripped.Spec.Strategy)
	assert.Equal(t, original.Spec.Failover.MaxRetries, roundTripped.Spec.Failover.MaxRetries)
	assert.Equal(t, original.Spec.Failover.RetryDelay, roundTripped.Spec.Failover.RetryDelay)
	assert.Equal(t, original.Spec.SessionAffinity.TTL, roundTripped.Spec.SessionAffinity.TTL)
	assert.Equal(t, original.Spec.CircuitBreaker.ResetTimeout, roundTripped.Spec.CircuitBreaker.ResetTimeout)
	assert.Equal(t, original.Status.ProviderCount, roundTripped.Status.ProviderCount)
	assert.Equal(t, original.Status.ReadyCount, roundTripped.Status.ReadyCount)
	require.Len(t, roundTripped.Status.Providers, 1)
	assert.Equal(t, original.Status.Providers[0].Name, roundTripped.Status.Providers[0].Name)
}

func TestMCPServerGroup_ConvertTo_WrongType(t *testing.T) {
	src := &MCPServerGroup{}
	err := src.ConvertTo(&v1alpha2.MCPServer{})
	assert.Error(t, err)
}

// --- MCPDiscoverySource round-trip tests ---

func TestMCPDiscoverySource_RoundTrip(t *testing.T) {
	original := &MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-discovery",
			Namespace: "default",
		},
		Spec: MCPDiscoverySourceSpec{
			Type:            DiscoveryTypeAnnotations,
			Mode:            DiscoveryModeAuthoritative,
			RefreshInterval: "2m0s",
			Paused:          false,
			NamespaceSelector: &NamespaceSelectorConfig{
				MatchLabels:       map[string]string{"env": "prod"},
				ExcludeNamespaces: []string{"kube-system"},
			},
			ConfigMapRef: &ConfigMapReference{
				Name: "providers", Namespace: "config", Key: "providers.yaml",
			},
			Annotations: &AnnotationDiscoveryConfig{
				PodSelector:         map[string]string{"app": "mcp"},
				ServiceSelector:     map[string]string{"svc": "mcp"},
				AnnotationPrefix:    "mcp-hangar.io",
				RequiredAnnotations: []string{"mcp-hangar.io/provider"},
			},
			ServiceDiscovery: &ServiceDiscoveryConfig{
				Selector: map[string]string{"type": "mcp"}, PortName: "mcp", Protocol: "https",
			},
			MCPServerTemplate: &MCPServerTemplateConfig{
				Metadata: &TemplateMetadata{
					Labels: map[string]string{"managed-by": "discovery"},
				},
				Spec: &MCPServerSpec{
					Mode:           MCPServerModeContainer,
					Image:          "default:latest",
					IdleTTL:        "10m0s",
					StartupTimeout: "1m0s",
				},
			},
			Filters: &DiscoveryFilters{
				IncludePatterns: []string{"math-.*"},
				ExcludePatterns: []string{"test-.*"},
				MaxProviders:    int32Ptr(50),
			},
			Ownership: &OwnershipConfig{
				Controller:    boolPtr(true),
				BlockDeletion: true,
			},
		},
		Status: MCPDiscoverySourceStatus{
			DiscoveredCount:    10,
			ManagedCount:       8,
			LastSyncTime:       &metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			LastSyncDuration:   "3s",
			LastSyncError:      "",
			NextSyncTime:       &metav1.Time{Time: time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC)},
			ObservedGeneration: 2,
			Conditions: []Condition{
				{Type: "Synced", Status: metav1.ConditionTrue, Reason: "SyncComplete", Message: "All discovered"},
			},
			DiscoveredMCPServers: []DiscoveredMCPServer{
				{Name: "math-provider", Source: "annotation:default/math-pod", Managed: true},
			},
		},
	}

	hub := &v1alpha2.MCPDiscoverySource{}
	require.NoError(t, original.ConvertTo(hub))

	// Verify v1alpha2 duration conversion
	assert.Equal(t, durationPtr(2*time.Minute), hub.Spec.RefreshInterval)
	assert.Equal(t, durationPtr(3*time.Second), hub.Status.LastSyncDuration)

	// Verify embedded template spec durations
	require.NotNil(t, hub.Spec.MCPServerTemplate)
	require.NotNil(t, hub.Spec.MCPServerTemplate.Spec)
	assert.Equal(t, durationPtr(10*time.Minute), hub.Spec.MCPServerTemplate.Spec.IdleTTL)
	assert.Equal(t, durationPtr(1*time.Minute), hub.Spec.MCPServerTemplate.Spec.StartupTimeout)

	roundTripped := &MCPDiscoverySource{}
	require.NoError(t, roundTripped.ConvertFrom(hub))

	assert.Equal(t, original.Spec.Type, roundTripped.Spec.Type)
	assert.Equal(t, original.Spec.Mode, roundTripped.Spec.Mode)
	assert.Equal(t, original.Spec.RefreshInterval, roundTripped.Spec.RefreshInterval)
	assert.Equal(t, original.Spec.Paused, roundTripped.Spec.Paused)
	assert.Equal(t, original.Spec.NamespaceSelector, roundTripped.Spec.NamespaceSelector)
	assert.Equal(t, original.Spec.ConfigMapRef, roundTripped.Spec.ConfigMapRef)
	assert.Equal(t, original.Spec.Annotations, roundTripped.Spec.Annotations)
	assert.Equal(t, original.Spec.ServiceDiscovery, roundTripped.Spec.ServiceDiscovery)

	// MCPServerTemplate round-trip (includes embedded duration conversions)
	require.NotNil(t, roundTripped.Spec.MCPServerTemplate)
	assert.Equal(t, original.Spec.MCPServerTemplate.Metadata, roundTripped.Spec.MCPServerTemplate.Metadata)
	require.NotNil(t, roundTripped.Spec.MCPServerTemplate.Spec)
	assert.Equal(t, original.Spec.MCPServerTemplate.Spec.Image, roundTripped.Spec.MCPServerTemplate.Spec.Image)
	assert.Equal(t, original.Spec.MCPServerTemplate.Spec.IdleTTL, roundTripped.Spec.MCPServerTemplate.Spec.IdleTTL)
	assert.Equal(t, original.Spec.MCPServerTemplate.Spec.StartupTimeout, roundTripped.Spec.MCPServerTemplate.Spec.StartupTimeout)

	assert.Equal(t, original.Spec.Filters, roundTripped.Spec.Filters)
	assert.Equal(t, original.Spec.Ownership, roundTripped.Spec.Ownership)
	assert.Equal(t, original.Status.DiscoveredCount, roundTripped.Status.DiscoveredCount)
	assert.Equal(t, original.Status.LastSyncDuration, roundTripped.Status.LastSyncDuration)
	require.Len(t, roundTripped.Status.DiscoveredMCPServers, 1)
	assert.Equal(t, original.Status.DiscoveredMCPServers[0].Name, roundTripped.Status.DiscoveredMCPServers[0].Name)
}

func TestMCPDiscoverySource_ConvertTo_WrongType(t *testing.T) {
	src := &MCPDiscoverySource{}
	err := src.ConvertTo(&v1alpha2.MCPServer{})
	assert.Error(t, err)
}

// --- Duration helper tests ---

func TestStringToDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *metav1.Duration
		wantErr bool
	}{
		{name: "empty", input: "", want: nil},
		{name: "seconds", input: "30s", want: durationPtr(30 * time.Second)},
		{name: "minutes", input: "5m", want: durationPtr(5 * time.Minute)},
		{name: "complex", input: "1h30m", want: durationPtr(90 * time.Minute)},
		{name: "invalid", input: "not-a-duration", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stringToDuration(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDurationToString(t *testing.T) {
	tests := []struct {
		name  string
		input *metav1.Duration
		want  string
	}{
		{name: "nil", input: nil, want: ""},
		{name: "seconds", input: durationPtr(30 * time.Second), want: "30s"},
		{name: "minutes", input: durationPtr(5 * time.Minute), want: "5m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, durationToString(tt.input))
		})
	}
}

// --- Condition conversion tests ---

func TestConditionsToStandard(t *testing.T) {
	now := metav1.Now()

	src := []Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: now, Reason: "OK", Message: "all good", ObservedGeneration: 3},
		{Type: "Degraded", Status: metav1.ConditionFalse, LastTransitionTime: now, Reason: "Stable", Message: "not degraded"},
	}

	dst := conditionsToStandard(src)
	require.Len(t, dst, 2)
	assert.Equal(t, "Ready", dst[0].Type)
	assert.Equal(t, metav1.ConditionTrue, dst[0].Status)
	assert.Equal(t, int64(3), dst[0].ObservedGeneration)

	// Nil input
	assert.Nil(t, conditionsToStandard(nil))
}

func TestConditionsFromStandard(t *testing.T) {
	now := metav1.Now()

	src := []metav1.Condition{
		{Type: "Available", Status: metav1.ConditionTrue, LastTransitionTime: now, Reason: "Running", Message: "OK", ObservedGeneration: 7},
	}

	dst := conditionsFromStandard(src)
	require.Len(t, dst, 1)
	assert.Equal(t, "Available", dst[0].Type)
	assert.Equal(t, int64(7), dst[0].ObservedGeneration)

	assert.Nil(t, conditionsFromStandard(nil))
}

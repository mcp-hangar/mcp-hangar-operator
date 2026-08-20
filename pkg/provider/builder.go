// Package provider contains utilities for building Kubernetes resources from MCPServer specs
package provider

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

const (
	// Labels
	LabelManagedBy   = "app.kubernetes.io/managed-by"
	LabelName        = "app.kubernetes.io/name"
	LabelInstance    = "app.kubernetes.io/instance"
	LabelComponent   = "app.kubernetes.io/component"
	LabelPartOf      = "app.kubernetes.io/part-of"
	LabelProvider    = "mcp-hangar.io/provider"
	LabelProviderUID = "mcp-hangar.io/provider-uid"

	// Annotations
	AnnotationGeneration = "mcp-hangar.io/generation"
	AnnotationConfigHash = "mcp-hangar.io/config-hash"

	// Discovery annotations. These are how a pod becomes a server in the
	// gateway: core's kubernetes discovery source skips any pod without
	// `enabled: "true"`, and the operator's client has no registration call, so
	// without these an MCPServer created through the CRD produced a Running pod
	// that the gateway never heard of -- while the CR reported Ready (#100).
	AnnotationDiscoveryEnabled = "mcp-hangar.io/enabled"
	AnnotationDiscoveryName    = "mcp-hangar.io/name"
	AnnotationDiscoveryMode    = "mcp-hangar.io/mode"
	AnnotationDiscoveryPort    = "mcp-hangar.io/port"

	// DiscoveryModeHTTP is what a container-mode MCPServer looks like from the
	// gateway's side: a pod serving HTTP at its own address. Core maps this to
	// a remote server pointed at the pod IP.
	DiscoveryModeHTTP = "http"

	// DefaultDiscoveryPort matches core's own default for the port annotation.
	// The MCPServer spec has no port field, so both sides have to agree on one
	// number; when the spec grows one, this is where it stops being a constant.
	DefaultDiscoveryPort = 8080

	// Container names
	ContainerProvider = "provider"

	// Default values
	DefaultManagerName = "mcp-hangar-operator"
)

// BuildPodForMCPServer creates a Pod spec from MCPServer
func BuildPodForMCPServer(provider *mcpv1alpha2.MCPServer) (*corev1.Pod, error) {
	if provider.Spec.Image == "" {
		return nil, fmt.Errorf("container mode requires image")
	}

	podName := provider.GetPodName()

	// Build main container
	container := buildContainer(provider)

	container.VolumeMounts = provider.Spec.VolumeMounts

	// Build Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   provider.Namespace,
			Labels:      buildLabels(provider),
			Annotations: buildAnnotations(provider),
		},
		Spec: corev1.PodSpec{
			Containers:                    []corev1.Container{container},
			Volumes:                       provider.Spec.Volumes,
			RestartPolicy:                 corev1.RestartPolicyNever, // Operator manages restarts
			ServiceAccountName:            provider.Spec.ServiceAccountName,
			NodeSelector:                  provider.Spec.NodeSelector,
			ImagePullSecrets:              provider.Spec.ImagePullSecrets,
			PriorityClassName:             provider.Spec.PriorityClassName,
			TerminationGracePeriodSeconds: getTerminationGracePeriod(provider),
		},
	}

	pod.Spec.Tolerations = provider.Spec.Tolerations
	pod.Spec.Affinity = provider.Spec.Affinity

	// Pod security context: the spec value wins whole, otherwise secure defaults.
	pod.Spec.SecurityContext = provider.Spec.PodSecurityContext
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = defaultPodSecurityContext()
	}

	return pod, nil
}

// buildContainer creates the main provider container
func buildContainer(provider *mcpv1alpha2.MCPServer) corev1.Container {
	container := corev1.Container{
		Name:            ContainerProvider,
		Image:           provider.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
	}

	// Command and args
	if len(provider.Spec.Command) > 0 {
		container.Command = provider.Spec.Command
	}
	if len(provider.Spec.Args) > 0 {
		container.Args = provider.Spec.Args
	}

	// Working directory
	if provider.Spec.WorkingDir != "" {
		container.WorkingDir = provider.Spec.WorkingDir
	}

	// Environment variables
	container.Env = buildEnvVars(provider)

	// Resources
	if provider.Spec.Resources != nil {
		container.Resources = *provider.Spec.Resources
	}

	// Container security context
	container.SecurityContext = provider.Spec.ContainerSecurityContext
	if container.SecurityContext == nil {
		container.SecurityContext = defaultContainerSecurityContext()
	}

	return container
}

// buildAnnotations creates the pod annotations, including the ones core's
// kubernetes discovery source reads.
//
// Stamping these is what connects the two halves. The operator creates the pod
// and never registers anything with the gateway -- its client has read, policy
// and delete calls, and no create -- so the gateway learns about a pod through
// discovery or not at all. `name` carries the MCPServer's own name rather than
// the pod's, because that is the identity the user wrote and the one they will
// look for in the gateway.
//
// Deliberately not stamped: `mcp-hangar.io/ttl`. Core reads that as the
// discovery TTL -- how long an entry survives without being seen again -- and
// the spec's `idleTTL` means how long an idle server stays running. Mapping one
// onto the other because the names rhyme would quietly deregister busy servers.
func buildAnnotations(provider *mcpv1alpha2.MCPServer) map[string]string {
	annotations := map[string]string{
		AnnotationGeneration: strconv.FormatInt(provider.Generation, 10),
	}

	if provider.Spec.Mode == mcpv1alpha2.MCPServerModeContainer {
		annotations[AnnotationDiscoveryEnabled] = "true"
		annotations[AnnotationDiscoveryName] = provider.Name
		annotations[AnnotationDiscoveryMode] = DiscoveryModeHTTP
		annotations[AnnotationDiscoveryPort] = strconv.Itoa(DefaultDiscoveryPort)
	}

	return annotations
}

// buildLabels creates standard labels for provider resources
func buildLabels(provider *mcpv1alpha2.MCPServer) map[string]string {
	labels := map[string]string{
		LabelManagedBy: DefaultManagerName,
		LabelName:      provider.Name,
		LabelInstance:  provider.Name,
		LabelComponent: "provider",
		LabelPartOf:    "mcp-hangar",
		LabelProvider:  provider.Name,
	}

	// Add provider UID for stronger ownership
	if provider.UID != "" {
		labels[LabelProviderUID] = string(provider.UID)
	}

	return labels
}

// buildEnvVars creates environment variables from provider spec
func buildEnvVars(provider *mcpv1alpha2.MCPServer) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:  "MCP_PROVIDER_NAME",
			Value: provider.Name,
		},
		{
			Name:  "MCP_PROVIDER_NAMESPACE",
			Value: provider.Namespace,
		},
		{
			Name: "MCP_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		{
			Name: "MCP_POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
	}

	// Add user-defined env vars
	return append(envVars, provider.Spec.Env...)
}

// defaultPodSecurityContext returns secure default pod security context
func defaultPodSecurityContext() *corev1.PodSecurityContext {
	runAsNonRoot := true
	runAsUser := int64(65534) // nobody
	fsGroup := int64(65534)

	return &corev1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		RunAsUser:    &runAsUser,
		FSGroup:      &fsGroup,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// defaultContainerSecurityContext returns secure default container security context
func defaultContainerSecurityContext() *corev1.SecurityContext {
	runAsNonRoot := true
	readOnlyRootFilesystem := true
	allowPrivilegeEscalation := false

	return &corev1.SecurityContext{
		RunAsNonRoot:             &runAsNonRoot,
		ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// getTerminationGracePeriod returns termination grace period in seconds.
// The v1alpha1 builder claimed to parse the string field and always returned
// the default; v1alpha2's *metav1.Duration is already parsed, so the spec
// value is honoured now (non-positive values fall back to the default).
func getTerminationGracePeriod(provider *mcpv1alpha2.MCPServer) *int64 {
	defaultGrace := int64(30)

	if provider.Spec.ShutdownGracePeriod == nil {
		return &defaultGrace
	}

	secs := int64(provider.Spec.ShutdownGracePeriod.Duration.Seconds())
	if secs <= 0 {
		return &defaultGrace
	}
	return &secs
}

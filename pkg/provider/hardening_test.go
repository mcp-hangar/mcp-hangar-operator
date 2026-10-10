package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func hardeningServer(image string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "hard", Namespace: "default"},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: image},
	}
}

// A partial security context in the spec replaced the restricted defaults
// whole: `fsGroup: 1000` alone dropped runAsNonRoot and seccomp (#211).
func TestBuildPod_PartialPodSecurityContextKeepsTheDefaults(t *testing.T) {
	s := hardeningServer("img@sha256:abc")
	s.Spec.PodSecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(int64(1000))}

	pod, err := BuildPodForMCPServer(s)
	require.NoError(t, err)
	sc := pod.Spec.SecurityContext
	assert.Equal(t, int64(1000), *sc.FSGroup, "the spec's field wins")
	require.NotNil(t, sc.RunAsNonRoot)
	assert.True(t, *sc.RunAsNonRoot, "the default stays")
	require.NotNil(t, sc.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)
}

func TestBuildPod_PartialContainerSecurityContextKeepsTheDefaults(t *testing.T) {
	s := hardeningServer("img@sha256:abc")
	s.Spec.ContainerSecurityContext = &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_BIND_SERVICE"}},
	}

	pod, err := BuildPodForMCPServer(s)
	require.NoError(t, err)
	sc := pod.Spec.Containers[0].SecurityContext
	assert.Equal(t, []corev1.Capability{"NET_BIND_SERVICE"}, sc.Capabilities.Add)
	assert.Equal(t, []corev1.Capability{"ALL"}, sc.Capabilities.Drop, "adding one capability must not re-grant the rest")
	assert.True(t, *sc.ReadOnlyRootFilesystem)
	assert.False(t, *sc.AllowPrivilegeEscalation)
	assert.Empty(t, s.Spec.ContainerSecurityContext.Capabilities.Drop, "the MCPServer's own spec is not written to")
}

func TestBuildPod_ExplicitValuesStillWin(t *testing.T) {
	s := hardeningServer("img@sha256:abc")
	s.Spec.ContainerSecurityContext = &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(false)}

	pod, err := BuildPodForMCPServer(s)
	require.NoError(t, err)
	assert.False(t, *pod.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem)
}

// No spec.resources made a BestEffort pod, evicted first under pressure.
func TestBuildPod_DefaultRequestsMakeItBurstable(t *testing.T) {
	pod, err := BuildPodForMCPServer(hardeningServer("img@sha256:abc"))
	require.NoError(t, err)
	r := pod.Spec.Containers[0].Resources
	assert.False(t, r.Requests.Cpu().IsZero())
	assert.False(t, r.Requests.Memory().IsZero())
	assert.Empty(t, r.Limits, "no guessed limits")
}

// A mutable tag kept whatever image the node had cached.
func TestBuildPod_PullPolicyFollowsPinning(t *testing.T) {
	tagged, err := BuildPodForMCPServer(hardeningServer("ghcr.io/org/app:latest"))
	require.NoError(t, err)
	assert.Equal(t, corev1.PullAlways, tagged.Spec.Containers[0].ImagePullPolicy)

	pinned, err := BuildPodForMCPServer(hardeningServer("ghcr.io/org/app@sha256:abc"))
	require.NoError(t, err)
	assert.Equal(t, corev1.PullIfNotPresent, pinned.Spec.Containers[0].ImagePullPolicy)
}

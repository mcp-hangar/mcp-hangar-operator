// A pod the operator creates has to be a pod the gateway can find.
//
// The operator's Hangar client has read, policy and delete calls and no create,
// so a server reaches the gateway through core's kubernetes discovery source or
// not at all -- and that source skips any pod without `mcp-hangar.io/enabled:
// "true"`. Before this, the builder stamped only `generation`, so an MCPServer
// created through the CRD produced a Running pod, a Ready CR, and a gateway that
// had never heard of it (#100).
//
// These tests pin the annotation contract on this side. It is shared with core,
// so it cannot be changed here alone.
package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
)

func containerServer(name string) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-servers", Generation: 3},
		Spec:       mcpv1alpha1.MCPServerSpec{Mode: mcpv1alpha1.MCPServerModeContainer, Image: "img:1"},
	}
}

func TestPodCarriesTheAnnotationsCoreDiscoversBy(t *testing.T) {
	pod, err := BuildPodForMCPServer(containerServer("operator-probe"))
	require.NoError(t, err)

	// The one core checks first. Without it the pod is invisible and every
	// other annotation here is decoration.
	assert.Equal(t, "true", pod.Annotations[AnnotationDiscoveryEnabled],
		"core skips any pod without this, so the gateway never learns the server exists")
	assert.Equal(t, "http", pod.Annotations[AnnotationDiscoveryMode])
	assert.Equal(t, "8080", pod.Annotations[AnnotationDiscoveryPort])
}

func TestTheServerIsNamedAfterTheCustomResource(t *testing.T) {
	// Not the pod name (`mcp-provider-<name>`). The user wrote `operator-probe`
	// and that is the id they will look for in the gateway.
	pod, err := BuildPodForMCPServer(containerServer("operator-probe"))
	require.NoError(t, err)

	assert.Equal(t, "operator-probe", pod.Annotations[AnnotationDiscoveryName])
	assert.Equal(t, "mcp-provider-operator-probe", pod.Name)
}

func TestTheExistingAnnotationsAreUntouched(t *testing.T) {
	// Generation drives the operator's own change detection; adding discovery
	// annotations must not displace it.
	pod, err := BuildPodForMCPServer(containerServer("operator-probe"))
	require.NoError(t, err)

	assert.Equal(t, "3", pod.Annotations[AnnotationGeneration])
}

func TestTtlIsNotMappedFromIdleTtl(t *testing.T) {
	// `mcp-hangar.io/ttl` is the *discovery* TTL: how long core keeps an entry
	// it has stopped seeing. It is not an idle timeout, and the builder must
	// never stamp it from a CR field -- the names rhyme and the meanings do not,
	// so mapping one to the other would deregister busy servers on a schedule.
	//
	// `spec.idleTTL` used to be the field that made this tempting; it was removed
	// in #120 because the operator never honoured it. The assertion outlives it:
	// the annotation must stay absent whatever the CR carries.
	server := containerServer("operator-probe")

	pod, err := BuildPodForMCPServer(server)
	require.NoError(t, err)

	_, present := pod.Annotations["mcp-hangar.io/ttl"]
	assert.False(t, present, "idleTTL and the discovery TTL are different quantities")
}

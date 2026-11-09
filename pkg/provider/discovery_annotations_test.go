// A pod the operator creates is not one core's discovery picks up.
//
// The operator registers every container-mode server with core itself, at its
// pod's address and under a Lease claim (#334). A pod still stamped with
// `mcp-hangar.io/enabled: "true"` would also be discovered by a core that runs
// kubernetes discovery: a second, unclaimed server under the same name.
package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

func containerServer(name string) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-servers", Generation: 3},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "img:1"},
	}
}

// The discovery annotations core read, by their wire names: the constants for
// the ones the operator no longer needs are gone.
var discoveryAnnotations = []string{
	"mcp-hangar.io/enabled", "mcp-hangar.io/name", "mcp-hangar.io/mode",
	"mcp-hangar.io/port", "mcp-hangar.io/path", "mcp-hangar.io/health-path",
}

func TestPodCarriesNoDiscoveryAnnotation(t *testing.T) {
	server := containerServer("operator-probe")
	pod, err := BuildPodForMCPServer(server)
	require.NoError(t, err)

	for _, a := range discoveryAnnotations {
		assert.NotContains(t, pod.Annotations, a)
	}
	assert.Equal(t, "3", pod.Annotations[AnnotationGeneration], "generation still drives change detection")
}

func TestTtlIsNotMappedFromIdleTtl(t *testing.T) {
	// `mcp-hangar.io/ttl` is the *discovery* TTL: how long core keeps an entry
	// it has stopped seeing. It is not an idle timeout, and the builder must
	// never stamp it from a CR field.
	pod, err := BuildPodForMCPServer(containerServer("operator-probe"))
	require.NoError(t, err)

	_, present := pod.Annotations["mcp-hangar.io/ttl"]
	assert.False(t, present, "idleTTL and the discovery TTL are different quantities")
}

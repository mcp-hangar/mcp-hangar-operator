package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// #334: the operator registers a plain container-mode server in core at its
// pod's address, under the same claim as a remote one, and its pod is no
// longer stamped for core's discovery.

func plainContainer(name string) *mcpv1alpha2.MCPServer {
	s := remoteCR("default", name, "")
	s.Spec.Mode = mcpv1alpha2.MCPServerModeContainer
	s.Spec.Image = "ghcr.io/example/box@sha256:" + keyA[7:]
	return s
}

func deploymentOf(t *testing.T, r *MCPServerReconciler, s *mcpv1alpha2.MCPServer) *appsv1.Deployment {
	t.Helper()
	dep := &appsv1.Deployment{}
	require.NoError(t, r.Get(t.Context(), types.NamespacedName{Name: provider.DeploymentName(s), Namespace: s.Namespace}, dep))
	return dep
}

func TestContainer_IsRegisteredAtItsPodAddress(t *testing.T) {
	core := newCore()
	s := plainContainer("box")
	r, _ := overHTTPS(core.serve(t), s)

	reconcileMCPServer(t, r, "box", "default")
	assert.Nil(t, core.servers["box"], "core takes no plain server without an endpoint")
	assert.NotNil(t, claimOf(t, r, "box"), "the name is claimed before the pod has an address")
	for _, a := range []string{provider.AnnotationDiscoveryEnabled, provider.AnnotationDiscoveryName} {
		assert.NotContains(t, deploymentOf(t, r, s).Spec.Template.Annotations, a)
	}

	podFor(r, t, s, "10.244.3.17")
	reconcileMCPServer(t, r, "box", "default")
	registered := core.servers["box"]
	require.NotNil(t, registered)
	assert.Equal(t, "http://10.244.3.17:8080/mcp", registered["endpoint"], "the endpoint core's discovery built")
	assert.Equal(t, "remote", registered["mode"])
	assert.NotContains(t, registered, "tls")
	assert.Len(t, core.sent("POST /api/mcp_servers/box/start"), 1, "started once registered")
	assert.Empty(t, core.sent(http.MethodDelete), "nothing was in core to remove first")

	reconcileMCPServer(t, r, "box", "default")
	assert.Len(t, core.sent("POST /api/mcp_servers/"), 1, "registered once")
	assert.Equal(t, "true", claimOf(t, r, "box").Annotations[claimStartedAnnotation])
}

func TestContainer_ItsEndpointFollowsThePod(t *testing.T) {
	core := newCore()
	s := plainContainer("box")
	r, _ := overHTTPS(core.serve(t), s)
	first := podFor(r, t, s, "10.244.3.17")
	reconcileMCPServer(t, r, "box", "default")
	require.Equal(t, "http://10.244.3.17:8080/mcp", core.servers["box"]["endpoint"])

	require.NoError(t, r.Delete(t.Context(), first))
	reconcileMCPServer(t, r, "box", "default")
	assert.Nil(t, core.servers["box"], "removed while no pod has the address")
	assert.NotNil(t, claimOf(t, r, "box"), "the name stays claimed")

	podFor(r, t, s, "fd00::17")
	reconcileMCPServer(t, r, "box", "default")
	assert.Equal(t, "http://[fd00::17]:8080/mcp", core.servers["box"]["endpoint"])
	assert.Equal(t, "http://[fd00::17]:8080/mcp", claimOf(t, r, "box").Annotations[claimEndpointAnnotation])
}

func TestContainer_AServerSwitchedOffIsRemoved(t *testing.T) {
	core := newCore()
	s := plainContainer("box")
	r, _ := overHTTPS(core.serve(t), s)
	podFor(r, t, s, "10.244.3.17")
	reconcileMCPServer(t, r, "box", "default")
	require.NotNil(t, core.servers["box"])

	s = getMCPServer(t, r, "box", "default")
	s.Spec.Replicas = ptr.To(int32(0))
	require.NoError(t, r.Update(t.Context(), s))
	reconcileMCPServer(t, r, "box", "default")

	assert.Nil(t, core.servers["box"])
}

func TestContainer_DeleteDeregistersOnlyWithTheClaim(t *testing.T) {
	core := newCore()
	s := plainContainer("box")
	r, _ := overHTTPS(core.serve(t), s)
	podFor(r, t, s, "10.244.3.17")
	reconcileMCPServer(t, r, "box", "default")
	require.NotNil(t, core.servers["box"])

	_, err := r.reconcileDelete(t.Context(), getMCPServer(t, r, "box", "default"))
	require.NoError(t, err)
	assert.Nil(t, core.servers["box"], "the claim holder's server is removed")
	assert.Nil(t, claimOf(t, r, "box"), "and the claim released")

	// A server core holds under the name for someone else: no claim, no delete.
	other := newCore()
	other.servers["box"] = map[string]any{"provenance": "human"}
	r, _ = overHTTPS(other.serve(t), plainContainer("box"))
	reconcileMCPServer(t, r, "box", "default")
	_, err = r.reconcileDelete(t.Context(), getMCPServer(t, r, "box", "default"))
	require.NoError(t, err)
	assert.NotNil(t, other.servers["box"])
	assert.Empty(t, other.sent(http.MethodDelete))
}

func TestContainer_AServerSomeoneElseRegisteredIsNotTakenOver(t *testing.T) {
	core := newCore()
	core.servers["box"] = map[string]any{"endpoint": "https://elsewhere:8443", "provenance": "human"}
	s := plainContainer("box")
	r := stampedForDiscovery(t, core, s, true, false)
	podFor(r, t, s, "10.244.3.17")

	reconcileMCPServer(t, r, "box", "default")

	assert.Empty(t, core.sent(http.MethodPut))
	assert.Empty(t, core.sent(http.MethodDelete))
	assert.Nil(t, claimOf(t, r, "box"))
	assert.Equal(t, ReasonNameTaken, degradedReason(t, r, "default", "box").Reason)
}

// stampedForDiscovery creates s's Deployment as an operator before #334 left
// it: its template stamped with mcp-hangar.io/name, or, once rewritten, only
// the marker this operator carries over.
func stampedForDiscovery(t *testing.T, core *pinningCore, s *mcpv1alpha2.MCPServer, template, marker bool) *MCPServerReconciler {
	t.Helper()
	dep, err := provider.BuildDeploymentForMCPServer(s)
	require.NoError(t, err)
	if template {
		dep.Spec.Template.Annotations[provider.AnnotationDiscoveryEnabled] = "true"
		dep.Spec.Template.Annotations[provider.AnnotationDiscoveryName] = s.Name
	}
	if marker {
		dep.Annotations[annotationDiscoveredAs] = s.Name
	}
	dep.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(s, mcpv1alpha2.GroupVersion.WithKind("MCPServer"))}
	r, _ := overHTTPS(core.serve(t), s)
	// Applied by the operator's field manager, as the older operator did, so
	// that a field the new one leaves out is removed.
	require.NoError(t, applyOwned(t.Context(), r.Client, dep))
	return r
}

func TestContainer_AServerDiscoveredFromItsOwnPodIsMoved(t *testing.T) {
	core := newCore()
	core.servers["box"] = map[string]any{"endpoint": "http://10.244.3.17:8080/mcp", "provenance": "discovery"}
	s := plainContainer("box")
	r := stampedForDiscovery(t, core, s, true, false)
	podFor(r, t, s, "10.244.3.17")

	reconcileMCPServer(t, r, "box", "default")

	assert.Len(t, core.sent(http.MethodPut), 1, "updated in place, which makes it a human's in core")
	assert.Empty(t, core.sent("POST /api/mcp_servers/"), "not registered a second time")
	assert.Empty(t, core.sent(http.MethodDelete))
	claim := claimOf(t, r, "box")
	require.NotNil(t, claim)
	assert.Equal(t, "http://10.244.3.17:8080/mcp", claim.Annotations[claimEndpointAnnotation])

	dep := deploymentOf(t, r, s)
	assert.NotContains(t, dep.Spec.Template.Annotations, provider.AnnotationDiscoveryName, "no longer stamped")
	assert.Equal(t, "box", dep.Annotations[annotationDiscoveredAs], "the evidence is carried over")
}

// A first pass that does not complete the move still rewrites the Deployment;
// the marker it carries over is what recognises the server on the next one.
func TestContainer_TheCarriedMarkerStillRecognisesADiscoveredServer(t *testing.T) {
	core := newCore()
	core.servers["box"] = map[string]any{"endpoint": "http://10.244.3.17:8080/mcp", "provenance": "discovery"}
	s := plainContainer("box")
	r := stampedForDiscovery(t, core, s, false, true)
	podFor(r, t, s, "10.244.3.17")

	reconcileMCPServer(t, r, "box", "default")

	assert.Len(t, core.sent(http.MethodPut), 1)
	assert.NotNil(t, claimOf(t, r, "box"))
}

// A core without a durable configuration store changes no endpoint in place:
// the moved server is removed and registered again on the next pass.
func TestContainer_AMoveCoreRefusesIsRegisteredAgain(t *testing.T) {
	core := newCore()
	core.updateStatus = http.StatusUnprocessableEntity
	core.servers["box"] = map[string]any{"endpoint": "http://10.244.3.17:8080/mcp", "provenance": "discovery"}
	s := plainContainer("box")
	r := stampedForDiscovery(t, core, s, true, false)
	podFor(r, t, s, "10.244.3.17")

	reconcileMCPServer(t, r, "box", "default")
	require.NotNil(t, claimOf(t, r, "box"), "claimed before the update")
	assert.Equal(t, ReasonRegistrationFailed, degradedReason(t, r, "default", "box").Reason)

	reconcileMCPServer(t, r, "box", "default")
	assert.Len(t, core.sent(http.MethodDelete), 1)
	assert.Len(t, core.sent("POST /api/mcp_servers/"), 1)
	assert.Equal(t, "http://10.244.3.17:8080/mcp", core.servers["box"]["endpoint"])
	assert.Nil(t, core.servers["box"]["provenance"], "registered by the operator, not discovered")
}

// A key whose role cannot register says so, and nothing is removed from core
// before a first registration: a role without mcp_servers:lifecycle would
// have its DELETE refused before the POST could report the reason.
func TestContainer_RoleWithoutWriteSaysSo(t *testing.T) {
	c := &registry{servers: map[string]string{}, forbidPost: true}
	s := plainContainer("box")
	r, _ := reconcilerAgainst(c.server(t), s)
	podFor(r, t, s, "10.244.3.17")

	reconcileMCPServer(t, r, "box", "default")
	reconcileMCPServer(t, r, "box", "default")

	degraded := degradedReason(t, r, "default", "box")
	require.NotNil(t, degraded)
	assert.Equal(t, ReasonRegistrationForbidden, degraded.Reason)
	_, calls := c.snapshot()
	for _, call := range calls {
		assert.NotContains(t, call, http.MethodDelete)
	}
}

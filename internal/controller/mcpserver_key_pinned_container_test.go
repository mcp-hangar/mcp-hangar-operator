package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// ADR-030 §4(a), (c), (f), T16: a key-pinned container is registered with core
// before its pod exists, its endpoint follows the pod, its pod is not
// discovered, and a server core discovered from its pod earlier is moved.

func keyPinnedContainer(name string) *mcpv1alpha2.MCPServer {
	s := keyPinnedCR(name, keyA)
	s.Spec.Mode = mcpv1alpha2.MCPServerModeContainer
	s.Spec.Endpoint = ""
	s.Spec.Image = "ghcr.io/example/payments@sha256:" + keyA[7:]
	return s
}

func podFor(r *MCPServerReconciler, t *testing.T, s *mcpv1alpha2.MCPServer, ip string) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: s.Name + "-pod-" + ip, Namespace: s.Namespace, Labels: provider.DeploymentSelector(s)},
		Status:     corev1.PodStatus{Phase: corev1.PodPending, PodIP: ip},
	}
	require.NoError(t, r.Create(t.Context(), pod))
	require.NoError(t, r.Status().Update(t.Context(), pod))
	return pod
}

func TestKeyPinnedContainer_IsRegisteredBeforeItsPodAndItsPodIsNotDiscovered(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	r, _ := overHTTPS(core.serve(t), keyPinnedContainer("payments"))

	reconcileMCPServer(t, r, "payments", "default")
	reconcileMCPServer(t, r, "payments", "default")

	registered := core.servers["payments"]
	require.NotNil(t, registered, "registered before any pod exists")
	assert.NotContains(t, registered, "endpoint")
	assert.Empty(t, core.sent("POST /api/mcp_servers/payments/start"), "not started without an endpoint")

	dep := &appsv1.Deployment{}
	require.NoError(t, r.Get(t.Context(), types.NamespacedName{Name: "mcp-provider-payments", Namespace: "default"}, dep))
	assert.NotContains(t, dep.Spec.Template.Annotations, provider.AnnotationDiscoveryEnabled)
	assert.NotContains(t, dep.Spec.Template.Annotations, provider.AnnotationDiscoveryName)
}

func TestKeyPinnedContainer_ItsEndpointFollowsThePod(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	s := keyPinnedContainer("payments")
	r, _ := overHTTPS(core.serve(t), s)
	reconcileMCPServer(t, r, "payments", "default")

	first := podFor(r, t, s, "10.244.3.17")
	reconcileMCPServer(t, r, "payments", "default")
	assert.Equal(t, "https://10.244.3.17:8080", core.servers["payments"]["endpoint"])
	reconcileMCPServer(t, r, "payments", "default")
	assert.Len(t, core.sent("POST /api/mcp_servers/payments/start"), 1, "started once it has an address")

	require.NoError(t, r.Delete(t.Context(), first))
	reconcileMCPServer(t, r, "payments", "default")
	assert.Nil(t, core.servers["payments"]["endpoint"], "cleared while no pod has the address")

	podFor(r, t, s, "fd00::17")
	reconcileMCPServer(t, r, "payments", "default")
	assert.Equal(t, "https://[fd00::17]:8080", core.servers["payments"]["endpoint"])
	assert.Len(t, core.sent(http.MethodPut), 3)
}

// discoveredServer seeds core with a server under the CR's name and the CR's
// own Deployment as it was before the key was declared, still stamping its
// pods for core's discovery.
func discoveredServer(t *testing.T, core *pinningCore, provenance any) *MCPServerReconciler {
	t.Helper()
	s := keyPinnedContainer("payments")
	plain := s.DeepCopy()
	plain.Spec.TLS = nil
	discovered, err := provider.BuildDeploymentForMCPServer(plain)
	require.NoError(t, err)
	discovered.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(s, mcpv1alpha2.GroupVersion.WithKind("MCPServer"))}
	core.servers["payments"] = map[string]any{"mcp_server_id": "payments", "endpoint": "http://10.244.3.17:8080", "provenance": provenance}
	r, _ := overHTTPS(core.serve(t), s)
	require.NoError(t, r.Create(t.Context(), discovered))
	return r
}

// Core 2.32.0 reports provenance; the move needs it to say discovery (#365).
func TestKeyPinnedContainer_MovesOnlyAServerCoreSaysWasDiscovered(t *testing.T) {
	discovered := newCore(hangar.PeerIdentityCapability)
	r := discoveredServer(t, discovered, "discovery")
	reconcileMCPServer(t, r, "payments", "default")
	assert.Len(t, discovered.sent(http.MethodPut), 1, "a discovered server is moved")

	human := newCore(hangar.PeerIdentityCapability)
	r = discoveredServer(t, human, "human")
	reconcileMCPServer(t, r, "payments", "default")
	assert.Empty(t, human.sent(http.MethodPut), "a server a human registered is not taken over")
	assert.Equal(t, ReasonNameTaken, degradedReason(t, r, "default", "payments").Reason)
}

func TestKeyPinnedContainer_AServerDiscoveredFromItsOwnPodIsMoved(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	s := keyPinnedContainer("payments")
	// Before the key was declared: a plain container, discovered by core.
	plain := s.DeepCopy()
	plain.Spec.TLS = nil
	discovered, err := provider.BuildDeploymentForMCPServer(plain)
	require.NoError(t, err)
	discovered.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(s, mcpv1alpha2.GroupVersion.WithKind("MCPServer"))}
	core.servers["payments"] = map[string]any{"mcp_server_id": "payments", "endpoint": "http://10.244.3.17:8080"}
	r, _ := overHTTPS(core.serve(t), s)
	require.NoError(t, r.Create(t.Context(), discovered))

	reconcileMCPServer(t, r, "payments", "default")

	assert.Len(t, core.sent(http.MethodPut), 1, "updated, not created again")
	assert.NotNil(t, core.servers["payments"]["tls"])
	assert.Equal(t, tlsDigest(coreTLS(s)), claimOf(t, r, "payments").Annotations[claimTLSAnnotation])
}

func TestKeyPinnedContainer_AServerSomeoneElseRegisteredIsNotTakenOver(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	core.servers["payments"] = map[string]any{"mcp_server_id": "payments", "endpoint": "https://elsewhere:8443"}
	r, _ := overHTTPS(core.serve(t), keyPinnedContainer("payments"))

	reconcileMCPServer(t, r, "payments", "default")

	assert.Empty(t, core.sent(http.MethodPut))
	assert.Equal(t, ReasonNameTaken, degradedReason(t, r, "default", "payments").Reason)
}

func TestKeyPinned_AGatedCRLeavesNothingPlainInCore(t *testing.T) {
	core := newCore(hangar.PeerIdentityCapability)
	s := remoteCR("default", "payments", "https://payments.svc.example:8443/mcp")
	r, _ := overHTTPS(core.serve(t), s)
	reconcileMCPServer(t, r, "payments", "default")
	require.NotNil(t, core.servers["payments"], "registered plain first")

	// The CR declares a key, and core cannot check one.
	core.capabilities = nil
	s = getMCPServer(t, r, "payments", "default")
	s.Spec.TLS = keyPinnedCR("payments", keyA).Spec.TLS
	require.NoError(t, r.Update(t.Context(), s))
	reconcileMCPServer(t, r, "payments", "default")

	assert.Nil(t, core.servers["payments"], "the plain registration is removed")
	assert.Nil(t, claimOf(t, r, "payments"), "and the name given back")
	assert.Equal(t, ReasonPeerIdentityNotCarried, degradedReason(t, r, "default", "payments").Reason)
}

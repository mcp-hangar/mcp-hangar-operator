package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// Until the operator sends core a key-pinned server's key list (#357), it must
// not run or register one: core would serve it as a plain server, its key
// checked by nothing, with the CR reading as if the declaration applied.

func pinned(s *mcpv1alpha2.MCPServer) *mcpv1alpha2.MCPServer {
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
		KeyRef: "lineage", SPKISHA256: []string{"sha256:" + "a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0"},
	}}
	return s
}

func TestPeerIdentity_ARemoteServerIsNotRegisteredPlain(t *testing.T) {
	c := &registry{servers: map[string]string{}}
	core := c.server(t)
	r, rec := reconcilerAgainst(core, pinned(remoteCR("default", "payments", "https://10.244.3.17:8443/mcp")))

	reconcileMCPServer(t, r, "payments", "default")
	reconcileMCPServer(t, r, "payments", "default")

	servers, calls := c.snapshot()
	assert.Empty(t, servers)
	assert.Empty(t, calls, "core is not asked at all")
	ready := apimeta.FindStatusCondition(getMCPServer(t, r, "payments", "default").Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, ReasonPeerIdentityNotCarried, ready.Reason)
	assert.Len(t, rec.Events, 1, "one Warning on the transition, not one per reconcile")
}

func TestPeerIdentity_AContainerServerGetsNoPod(t *testing.T) {
	s := pinned(remoteCR("default", "payments", ""))
	s.Spec.Mode = mcpv1alpha2.MCPServerModeContainer
	s.Spec.Image = "ghcr.io/example/payments@sha256:" + "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"
	r := newMCPServerReconciler(s)

	reconcileMCPServer(t, r, "payments", "default")

	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-payments", Namespace: "default"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err), "its pod would be discovered by core as a plain server")
}

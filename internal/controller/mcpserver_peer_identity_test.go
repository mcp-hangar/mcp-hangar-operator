package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// Until the operator runs a key-pinned container with its key list, it must not
// run one: core would discover its pod as a plain server, its key checked by
// nothing, with the CR reading as if the declaration applied.

func pinned(s *mcpv1alpha2.MCPServer) *mcpv1alpha2.MCPServer {
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
		KeyRef: "lineage", SPKISHA256: []string{"sha256:" + "a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0"},
	}}
	return s
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

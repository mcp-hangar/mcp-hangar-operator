package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
)

// claimTLSAnnotation is a digest of the tls block the server was registered
// with, so a changed key list is delivered to core rather than missed.
const claimTLSAnnotation = "mcp-hangar.io/registered-tls"

// keyPinned reports whether the CR declares a peer identity.
func keyPinned(mcpServer *mcpv1alpha2.MCPServer) bool {
	return mcpServer.Spec.TLS != nil && mcpServer.Spec.TLS.PeerIdentity != nil
}

// coreTLS is the CR's tls block in core's form, or nil for a server with no
// peer identity. Core takes the age in seconds.
func coreTLS(mcpServer *mcpv1alpha2.MCPServer) map[string]any {
	if !keyPinned(mcpServer) {
		return nil
	}
	identity := mcpServer.Spec.TLS.PeerIdentity
	tls := map[string]any{"peer_identity": map[string]any{"key_ref": identity.KeyRef, "spki_sha256": identity.SPKISHA256}}
	if age := mcpServer.Spec.TLS.MaxConnectionAge; age != nil {
		tls["max_connection_age"] = age.Seconds()
	}
	return tls
}

// tlsDigest identifies a tls block across reconciles; empty for none.
func tlsDigest(tls map[string]any) string {
	if tls == nil {
		return ""
	}
	encoded, _ := json.Marshal(tls) // map keys are sorted, so the encoding is stable
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// peerIdentityRefusal says why a key-pinned CR must not be registered with this
// core, or "" when it may (ADR-030 §4e). Each would have core serve the server
// without its key checked, or have the key list cross an unprotected channel.
func (r *MCPServerReconciler) peerIdentityRefusal(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (string, string, error) {
	if mcpServer.Spec.TLS.PeerIdentity.Issuance != "" {
		return ReasonPeerIdentityNotCarried, "spec.tls.peerIdentity.issuance is declared, and core does not yet record " +
			"a key issuance; remove it to register the server with its key list", nil
	}
	if !r.HangarClient.OverHTTPS() {
		return ReasonPeerIdentityNotCarried, "a key list is delivered to core only over https; set hangar.url to an " +
			"https:// address with hangar.tls.ca", nil
	}
	capabilities, err := r.HangarClient.Capabilities(ctx)
	if err != nil {
		return "", "", err
	}
	if !slices.Contains(capabilities, hangar.PeerIdentityCapability) {
		return ReasonPeerIdentityNotCarried, "core does not advertise the peer_identity capability, so it would " +
			"ignore the key list and serve the server unchecked; upgrade core", nil
	}
	return "", "", nil
}

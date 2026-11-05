package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/hangar"
	"github.com/mcp-hangar/operator/pkg/provider"
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
	peer := map[string]any{"key_ref": identity.KeyRef, "spki_sha256": identity.SPKISHA256}
	if identity.Issuance != "" {
		peer["issuance"] = string(identity.Issuance)
	}
	tls := map[string]any{"peer_identity": peer}
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
	if mcpServer.Spec.TLS.PeerIdentity.Issuance != "" && !slices.Contains(capabilities, hangar.DeclaredKeyIssuanceCapability) {
		return ReasonPeerIdentityNotCarried, "spec.tls.peerIdentity.issuance is declared, and core does not advertise " +
			"the declared_key_issuance capability, so it would refuse the key list; upgrade core or remove issuance", nil
	}
	return "", "", nil
}

// containerPath is the path core reaches a plain container's MCP server on:
// what core's discovery used for the operator's pods, mcp-hangar.io/path
// unset and so /mcp, which the operator never stamped.
const containerPath = "/mcp"

// annotationDiscoveredAs, on a Deployment this operator controls, records that
// its pod template once carried mcp-hangar.io/name for core's discovery. The
// operator writes it, from that annotation, when it stops stamping the
// template (#334), so a server core discovered before the upgrade is still
// recognised once the template no longer says so.
const annotationDiscoveredAs = "mcp-hangar.io/discovered-as"

// podEndpoint is where core reaches a container-mode server: its pod's
// address, on the port core's discovery used. A key-pinned server is reached
// over https, as it always was (ADR-030 §4a); a plain one over http at
// containerPath, the endpoint core's discovery built for it (#334). Empty
// while the server is off or its pod has no address yet; the endpoint is then
// cleared in core, so calls are refused rather than sent to an address another
// pod may inherit.
func (r *MCPServerReconciler) podEndpoint(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) (string, error) {
	if mcpServer.IsCold() {
		return "", nil
	}
	pod, err := r.currentPod(ctx, mcpServer)
	if err != nil || pod == nil || pod.Status.PodIP == "" {
		return "", err
	}
	hostPort := net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(provider.DefaultDiscoveryPort))
	if keyPinned(mcpServer) {
		return "https://" + hostPort, nil
	}
	return "http://" + hostPort + containerPath, nil
}

// discoveredFromItsOwnPod reports whether core's server under this CR's name
// came from this CR's own pod, by core's discovery. Two things must agree:
//
//   - the Deployment this CR controls stamps, or stamped, its pods with
//     mcp-hangar.io/name: the template annotation itself, which an operator
//     before #334 wrote, or annotationDiscoveredAs, which this operator
//     carries over from it when it rewrites the template. Both are written by
//     the operator on an object the CR controls. The carried marker is what
//     keeps the decision stable across the upgrade: the registration runs
//     before the Deployment is rewritten, but a first pass that fails (core
//     unreachable, say) would otherwise leave the next one nothing to go on;
//   - core reports the server's provenance as discovery. Core exposes no pod
//     or namespace of a discovered server, so provenance is the only signal
//     from core's side; a server a human registered is never discovery, and
//     is not this CR's to move.
//
// A core before 2.32.0 reports no provenance, and the Deployment alone
// decides there.
func (r *MCPServerReconciler) discoveredFromItsOwnPod(ctx context.Context, mcpServer *mcpv1alpha2.MCPServer) bool {
	deployment := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: mcpServer.Namespace, Name: provider.DeploymentName(mcpServer)}
	if err := r.Get(ctx, key, deployment); err != nil || !metav1.IsControlledBy(deployment, mcpServer) {
		return false
	}
	if discoveredAs(deployment) != mcpServer.Name {
		return false
	}
	provenance, err := r.HangarClient.GetMCPServerProvenance(ctx, mcpServer.Name)
	if err != nil {
		return false
	}
	return provenance == "" || provenance == "discovery"
}

// discoveredAs is the server name core's discovery knew the Deployment's pods
// by, or "" when they were never stamped.
func discoveredAs(deployment *appsv1.Deployment) string {
	if name := deployment.Spec.Template.Annotations[provider.AnnotationDiscoveryName]; name != "" {
		return name
	}
	return deployment.Annotations[annotationDiscoveredAs]
}

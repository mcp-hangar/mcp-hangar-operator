package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// ADR-030 T14 and T18: a key pin has one canonical form, a peer identity names
// its lineage, and a discovery source cannot stamp one key list on every
// server it yields. Every rejection is the apiserver's, with no webhook.

const (
	pinA = "sha256:9f2c000000000000000000000000000000000000000000000000000000000000"
	pinB = "sha256:4b10000000000000000000000000000000000000000000000000000000000000"
)

func peerIdentityServer(name string, tls map[string]any) map[string]any {
	return map[string]any{"mode": "remote", "endpoint": "https://10.244.3.17:8443/mcp", "tls": tls}
}

func TestCRDRules_APeerIdentityIsAdmittedInItsCanonicalForm(t *testing.T) {
	s := celServer("cel-peer-ok", mcpv1alpha2.MCPServerModeRemote)
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{
		PeerIdentity:     &mcpv1alpha2.PeerIdentity{KeyRef: "kbs:///default/payments/tls-key", SPKISHA256: []string{pinA, pinB}},
		MaxConnectionAge: &metav1.Duration{Duration: 3600_000_000_000},
	}
	require.NoError(t, createAndCleanup(t, s))

	attested := celServer("cel-peer-attested", mcpv1alpha2.MCPServerModeRemote)
	attested.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
		KeyRef: "lineage", SPKISHA256: []string{pinA}, Issuance: mcpv1alpha2.PeerKeyIssuanceKBSAttested,
	}}
	require.NoError(t, createAndCleanup(t, attested), "a remote server needs no runtimeClassName")
}

func TestCRDRules_APeerIdentityThatWouldVerifyNothingIsRejected(t *testing.T) {
	cases := map[string]struct {
		identity map[string]any
		want     string
	}{
		"no-pins":       {map[string]any{"keyRef": "k", "spkiSha256": []any{}}, "spkiSha256"},
		"no-key-ref":    {map[string]any{"spkiSha256": []any{pinA}}, "keyRef"},
		"empty-key-ref": {map[string]any{"keyRef": "", "spkiSha256": []any{pinA}}, "keyRef"},
		"uppercase":     {map[string]any{"keyRef": "k", "spkiSha256": []any{"sha256:9F2C" + pinA[11:]}}, "spkiSha256"},
		"base64":        {map[string]any{"keyRef": "k", "spkiSha256": []any{"n6wAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}, "spkiSha256"},
		"other-algo":    {map[string]any{"keyRef": "k", "spkiSha256": []any{"sha512:" + pinA[7:]}}, "spkiSha256"},
		"issuance":      {map[string]any{"keyRef": "k", "spkiSha256": []any{pinA}, "issuance": "self-signed"}, "issuance"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			u := celServerUnstructured("cel-peer-"+name, peerIdentityServer(name, map[string]any{"peerIdentity": c.identity}))
			requireRejected(t, createAndCleanup(t, u), c.want)
		})
	}
}

func TestCRDRules_AMaxConnectionAgeNeedsAPeerIdentityAndMustBePositive(t *testing.T) {
	alone := celServerUnstructured("cel-age-alone", peerIdentityServer("alone", map[string]any{"maxConnectionAge": "1h"}))
	requireRejected(t, createAndCleanup(t, alone), "maxConnectionAge applies only with spec.tls.peerIdentity")

	zero := celServerUnstructured("cel-age-zero", peerIdentityServer("zero", map[string]any{
		"maxConnectionAge": "0s",
		"peerIdentity":     map[string]any{"keyRef": "k", "spkiSha256": []any{pinA}},
	}))
	requireRejected(t, createAndCleanup(t, zero), "must be a positive duration")
}

func TestCRDRules_AKeyPinnedRemoteServerNeedsHTTPS(t *testing.T) {
	s := celServer("cel-peer-http", mcpv1alpha2.MCPServerModeRemote)
	s.Spec.Endpoint = "http://10.244.3.17:8080/mcp"
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{KeyRef: "k", SPKISHA256: []string{pinA}}}
	requireRejected(t, createAndCleanup(t, s), "spec.endpoint must be https when spec.tls.peerIdentity is set")

	plain := celServer("cel-plain-http", mcpv1alpha2.MCPServerModeRemote)
	plain.Spec.Endpoint = "http://10.244.3.17:8080/mcp"
	require.NoError(t, createAndCleanup(t, plain), "a server without a peer identity keeps http")
}

func TestCRDRules_ADiscoveryTemplateCannotCarryAPeerIdentity(t *testing.T) {
	identity := &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{KeyRef: "k", SPKISHA256: []string{pinA}}}
	sources := map[string]mcpv1alpha2.MCPDiscoverySourceSpec{
		"pod": {Type: mcpv1alpha2.DiscoveryTypeAnnotations, Paused: true},
		"namespace": {Type: mcpv1alpha2.DiscoveryTypeNamespace, Paused: true, MCPServerTemplate: &mcpv1alpha2.MCPServerTemplateConfig{
			Spec: &mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeRemote, Endpoint: "https://svc.example:8443"},
		}},
	}
	for name, spec := range sources {
		t.Run(name, func(t *testing.T) {
			if spec.MCPServerTemplate == nil {
				spec.MCPServerTemplate = &mcpv1alpha2.MCPServerTemplateConfig{Spec: &mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer}}
			}
			spec.MCPServerTemplate.Spec.TLS = identity
			d := &mcpv1alpha2.MCPDiscoverySource{ObjectMeta: metav1.ObjectMeta{Name: "cel-template-peer-" + name, Namespace: "default"}, Spec: spec}
			requireRejected(t, createAndCleanup(t, d), "providerTemplate.spec.tls.peerIdentity is not allowed")
		})
	}
}

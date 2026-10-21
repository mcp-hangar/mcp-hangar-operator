package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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

func TestCRDRules_AnAttestedContainerNeedsARuntimeClass(t *testing.T) {
	attested := func(name string) *mcpv1alpha2.MCPServer {
		s := celServer(name, mcpv1alpha2.MCPServerModeContainer)
		s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
			KeyRef: "kbs:///default/payments/tls-key", SPKISHA256: []string{pinA}, Issuance: mcpv1alpha2.PeerKeyIssuanceKBSAttested,
		}}
		return s
	}
	requireRejected(t, createAndCleanup(t, attested("cel-attested-no-class")), "spec.runtimeClassName is required")

	require.NoError(t, createAndCleanup(t, kbsAttestedContainer("cel-attested-class")))

	operatorManaged := attested("cel-container-managed-keys")
	operatorManaged.Spec.TLS.PeerIdentity.Issuance = ""
	require.NoError(t, createAndCleanup(t, operatorManaged), "keys the operator manages need no runtime class")
}

const attestedKeyRef = "kbs:///default/payments/tls-key"

// kbsAttestedContainer is a container-mode server that satisfies every rule
// ADR-031 §3 sets for issuance kbs-attested.
func kbsAttestedContainer(name string) *mcpv1alpha2.MCPServer {
	s := celServer(name, mcpv1alpha2.MCPServerModeContainer)
	s.Spec.Image = "ghcr.io/example/provider@sha256:" + strings.Repeat("0", 64)
	class := "kata-qemu-snp"
	s.Spec.RuntimeClassName = &class
	automount := false
	s.Spec.AutomountServiceAccountToken = &automount
	s.Spec.InitData = "version = \"0.1.0\"\nalgorithm = \"sha256\"\n[data]\n\"key_ref\" = \"" + attestedKeyRef + "\"\n"
	s.Spec.TLS = &mcpv1alpha2.MCPServerTLS{PeerIdentity: &mcpv1alpha2.PeerIdentity{
		KeyRef: attestedKeyRef, SPKISHA256: []string{pinA}, Issuance: mcpv1alpha2.PeerKeyIssuanceKBSAttested,
	}}
	return s
}

// ADR-031 T28: on a kbs-attested container server the CRD refuses what would
// make the init-data say less than it seems to, and refuses init-data anywhere
// else.
func TestCRDRules_AnAttestedContainerIsHeldToItsInitData(t *testing.T) {
	cases := map[string]struct {
		mutate func(*mcpv1alpha2.MCPServer)
		want   string
	}{
		"no-init-data": {func(s *mcpv1alpha2.MCPServer) { s.Spec.InitData = "" }, "spec.initData is required"},
		"init-data-without-key-ref": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.InitData = "version = \"0.1.0\"\n"
		}, "must name spec.tls.peerIdentity.keyRef"},
		"tagged-image": {func(s *mcpv1alpha2.MCPServer) { s.Spec.Image = "ghcr.io/example/provider:1.0" }, "digest-pinned spec.image"},
		"env-from-secret": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.Env = []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k"},
			}}}
		}, "cannot take spec.env from a Secret"},
		"configmap-volume": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.Volumes = []corev1.Volume{{Name: "c", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "c"}},
			}}}
		}, "takes only emptyDir volumes"},
		"secret-volume": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.Volumes = []corev1.Volume{{Name: "s", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "s"}}}}
		}, "takes only emptyDir volumes"},
		"projected-volume": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.Volumes = []corev1.Volume{{Name: "p", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{}}}}
		}, "takes only emptyDir volumes"},
		"token-unset":   {func(s *mcpv1alpha2.MCPServer) { s.Spec.AutomountServiceAccountToken = nil }, "automountServiceAccountToken: false"},
		"token-mounted": {func(s *mcpv1alpha2.MCPServer) { yes := true; s.Spec.AutomountServiceAccountToken = &yes }, "automountServiceAccountToken: false"},
		"too-long": {func(s *mcpv1alpha2.MCPServer) {
			s.Spec.InitData = attestedKeyRef + strings.Repeat("#", 98304)
		}, "initData"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := kbsAttestedContainer("cel-init-data-" + name)
			c.mutate(s)
			requireRejected(t, createAndCleanup(t, s), c.want)
		})
	}

	t.Run("compliant", func(t *testing.T) {
		s := kbsAttestedContainer("cel-init-data-ok")
		s.Spec.Env = []corev1.EnvVar{{Name: "MODE", Value: "strict"}}
		s.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		require.NoError(t, createAndCleanup(t, s))
	})
}

func TestCRDRules_InitDataIsRefusedOffAnAttestedContainer(t *testing.T) {
	remote := celServer("cel-init-data-remote", mcpv1alpha2.MCPServerModeRemote)
	remote.Spec.InitData = "version = \"0.1.0\"\n"
	requireRejected(t, createAndCleanup(t, remote), "spec.initData applies only to")

	managed := kbsAttestedContainer("cel-init-data-managed-keys")
	managed.Spec.TLS.PeerIdentity.Issuance = ""
	requireRejected(t, createAndCleanup(t, managed), "spec.initData applies only to")

	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "cel-template-init-data", Namespace: "default"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{Type: mcpv1alpha2.DiscoveryTypeAnnotations, Paused: true, MCPServerTemplate: &mcpv1alpha2.MCPServerTemplateConfig{
			Spec: &mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, InitData: "x"},
		}},
	}
	requireRejected(t, createAndCleanup(t, d), "providerTemplate.spec.initData is not allowed")
}

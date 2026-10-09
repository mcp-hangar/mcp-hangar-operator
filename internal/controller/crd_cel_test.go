package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// The structural MCPServer, MCPDiscoverySource and MCPEgressPolicy rules are
// CRD schema and CEL rules (#196). They used to live only in the Go webhooks,
// which are off by default, so with the webhook off the apiserver stored
// anything. No webhook runs in this suite: every rejection below is the
// apiserver's, from the CRDs this repo ships.

func celServer(name string, mode mcpv1alpha2.MCPServerMode) *mcpv1alpha2.MCPServer {
	s := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       mcpv1alpha2.MCPServerSpec{Mode: mode},
	}
	if mode == mcpv1alpha2.MCPServerModeContainer {
		s.Spec.Image = "ghcr.io/example/provider:1.0"
	} else {
		s.Spec.Endpoint = "https://api.example.com/mcp"
	}
	return s
}

// celServerUnstructured builds an MCPServer the typed client could not send:
// an empty string survives (omitempty would drop it) and a duration need not
// parse.
func celServerUnstructured(name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mcp-hangar.io", Version: "v1alpha2", Kind: "MCPServer"})
	u.SetName(name)
	u.SetNamespace("default")
	return u
}

func createAndCleanup(t *testing.T, obj client.Object) error {
	t.Helper()
	err := k8sClient.Create(ctx, obj)
	if err == nil {
		t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}
	return err
}

func requireRejected(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err, "the apiserver must refuse this with the webhook off")
	assert.Contains(t, err.Error(), want)
}

func TestCRDRules_ContainerModeNeedsAnImage(t *testing.T) {
	s := celServer("cel-no-image", mcpv1alpha2.MCPServerModeContainer)
	s.Spec.Image = ""
	requireRejected(t, createAndCleanup(t, s), "spec.image is required when mode is container")

	empty := celServerUnstructured("cel-empty-image", map[string]any{"mode": "container", "image": ""})
	requireRejected(t, createAndCleanup(t, empty), "spec.image is required when mode is container")

	require.NoError(t, createAndCleanup(t, celServer("cel-image", mcpv1alpha2.MCPServerModeContainer)))
}

func TestCRDRules_RemoteModeNeedsAnHTTPEndpoint(t *testing.T) {
	missing := celServer("cel-no-endpoint", mcpv1alpha2.MCPServerModeRemote)
	missing.Spec.Endpoint = ""
	requireRejected(t, createAndCleanup(t, missing), "spec.endpoint is required when mode is remote")

	for i, bad := range []string{"javascript:alert(1)", "/only/path", "not-a-url", "ftp://files.example.com/", "http://"} {
		s := celServer("cel-bad-endpoint-"+string(rune('a'+i)), mcpv1alpha2.MCPServerModeRemote)
		s.Spec.Endpoint = bad
		requireRejected(t, createAndCleanup(t, s), "spec.endpoint must be an absolute http or https URL with a host")
	}

	for i, good := range []string{"https://api.example.com/mcp", "http://mcp.tools.svc:8080"} {
		s := celServer("cel-good-endpoint-"+string(rune('a'+i)), mcpv1alpha2.MCPServerModeRemote)
		s.Spec.Endpoint = good
		require.NoError(t, createAndCleanup(t, s), good)
	}

	// The endpoint is ignored in container mode (the webhook warns); it is
	// not a reason to refuse the object.
	c := celServer("cel-container-endpoint", mcpv1alpha2.MCPServerModeContainer)
	c.Spec.Endpoint = "not-a-url"
	require.NoError(t, createAndCleanup(t, c))
}

func TestCRDRules_DurationsMustBeNonNegative(t *testing.T) {
	s := celServer("cel-neg-startup", mcpv1alpha2.MCPServerModeContainer)
	s.Spec.StartupTimeout = &metav1.Duration{Duration: -5 * time.Second}
	requireRejected(t, createAndCleanup(t, s), "must be a non-negative duration")

	s = celServer("cel-neg-shutdown", mcpv1alpha2.MCPServerModeContainer)
	s.Spec.ShutdownGracePeriod = &metav1.Duration{Duration: -time.Second}
	requireRejected(t, createAndCleanup(t, s), "must be a non-negative duration")

	// A value that does not parse used to be stored, after which the typed
	// client could not decode the object.
	garbage := celServerUnstructured("cel-garbage-dur", map[string]any{
		"mode": "container", "image": "ghcr.io/example/provider:1.0", "startupTimeout": "banana",
	})
	require.Error(t, createAndCleanup(t, garbage))

	s = celServer("cel-good-dur", mcpv1alpha2.MCPServerModeContainer)
	s.Spec.StartupTimeout = &metav1.Duration{Duration: 90 * time.Second}
	s.Spec.ShutdownGracePeriod = &metav1.Duration{}
	require.NoError(t, createAndCleanup(t, s))
}

func TestCRDRules_ExpectedToolsAreNonEmptyAndUnique(t *testing.T) {
	withTools := func(name string, tools ...string) *mcpv1alpha2.MCPServer {
		s := celServer(name, mcpv1alpha2.MCPServerModeContainer)
		s.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
			Tools: &mcpv1alpha2.ToolCapabilitiesSpec{ExpectedTools: tools},
		}
		return s
	}

	requireRejected(t, createAndCleanup(t, withTools("cel-dup-tools", "calc", "search", "calc")),
		"expectedTools must not contain duplicates")
	requireRejected(t, createAndCleanup(t, withTools("cel-empty-tool", "calc", "")), "expectedTools")
	require.NoError(t, createAndCleanup(t, withTools("cel-tools", "calc", "search")))
}

func TestCRDRules_EgressCIDRMustBeWellFormed(t *testing.T) {
	withCIDR := func(name, cidr string) *mcpv1alpha2.MCPServer {
		s := celServer(name, mcpv1alpha2.MCPServerModeContainer)
		s.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
			Network: &mcpv1alpha2.NetworkCapabilitiesSpec{
				Egress: []mcpv1alpha2.EgressRuleSpec{{Host: "upstream", CIDR: cidr}},
			},
		}
		return s
	}

	for i, bad := range []string{"10.0.0.0/33", "256.0.0.0/8", "10.0.0.0", "example.com/8", "fd00::/129"} {
		requireRejected(t, createAndCleanup(t, withCIDR("cel-bad-cidr-"+string(rune('a'+i)), bad)), "cidr")
	}
	for i, good := range []string{"10.0.0.0/8", "203.0.113.7/32", "0.0.0.0/0", "fd00::/8", "2001:db8::1/128"} {
		require.NoError(t, createAndCleanup(t, withCIDR("cel-good-cidr-"+string(rune('a'+i)), good)), good)
	}
}

func TestCRDRules_ModeIsImmutable(t *testing.T) {
	s := celServer("cel-mode-flip", mcpv1alpha2.MCPServerModeContainer)
	require.NoError(t, createAndCleanup(t, s))

	flipped := s.DeepCopy()
	flipped.Spec.Mode = mcpv1alpha2.MCPServerModeRemote
	flipped.Spec.Endpoint = "https://api.example.com/mcp"
	requireRejected(t, k8sClient.Update(ctx, flipped), "spec.mode is immutable")

	// The transition rule must not get in the way of ordinary updates.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s))
	s.Labels = map[string]string{"team": "tools"}
	replicas := int32(2)
	s.Spec.Replicas = &replicas
	require.NoError(t, k8sClient.Update(ctx, s))

	s.Status.State = mcpv1alpha2.MCPServerStateReady
	require.NoError(t, k8sClient.Status().Update(ctx, s))
}

func TestCRDRules_DiscoveryConfigMapSourceNeedsARef(t *testing.T) {
	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "cel-cm-no-ref", Namespace: "default"},
		Spec:       mcpv1alpha2.MCPDiscoverySourceSpec{Type: mcpv1alpha2.DiscoveryTypeConfigMap, Paused: true},
	}
	requireRejected(t, createAndCleanup(t, d), "spec.configMapRef is required when spec.type is ConfigMap")

	d = d.DeepCopy()
	d.Name = "cel-cm-ref"
	d.Spec.ConfigMapRef = &mcpv1alpha2.ConfigMapReference{Name: "providers"}
	require.NoError(t, createAndCleanup(t, d))
}

func TestCRDRules_DiscoveryTemplateMayLeaveTheImageToTheEntry(t *testing.T) {
	// The MCPServer mode rules sit on MCPServer.spec, not on MCPServerSpec,
	// because providerTemplate.spec embeds the same type and a template
	// legitimately leaves the image to each discovered entry.
	d := &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "cel-template", Namespace: "default"},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
			Paused:       true,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "providers"},
			MCPServerTemplate: &mcpv1alpha2.MCPServerTemplateConfig{
				Spec: &mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer},
			},
		},
	}
	require.NoError(t, createAndCleanup(t, d))

	// ... and the template's mode stays editable. The discovery reconciler
	// writes the object too, so retry on a conflict.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			return err
		}
		d.Spec.MCPServerTemplate.Spec.Mode = mcpv1alpha2.MCPServerModeRemote
		return k8sClient.Update(ctx, d)
	}))
}

func TestCRDRules_EgressPolicyTargetRefIsImmutable(t *testing.T) {
	p := testPolicy("cel-retarget", "default")
	require.NoError(t, createAndCleanup(t, p))

	for _, ref := range []mcpv1alpha2.EgressTargetRef{
		{Kind: "MCPServer", Name: "other"},
		{Kind: "MCPServerGroup", Name: "srv"},
	} {
		retargeted := p.DeepCopy()
		retargeted.Spec.TargetRef = ref
		err := k8sClient.Update(ctx, retargeted)
		requireRejected(t, err, "spec.targetRef is immutable")
	}

	// Moving from Audit to Enforce is the normal life of a policy.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p))
	p.Spec.Mode = mcpv1alpha2.EgressPolicyModeEnforce
	require.NoError(t, k8sClient.Update(ctx, p))
}

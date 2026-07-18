package webhook_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// TestMain baselines the image-digest gate to "off" for the whole webhook test
// suite: the pre-existing tests use mutable-tag images and assert no warnings,
// and only the dedicated tests below opt into a "warn"/"block" policy.
func TestMain(m *testing.M) {
	_ = webhook.SetImageDigestPolicy("off")
	os.Exit(m.Run())
}

const (
	pinnedImage  = "ghcr.io/org/app@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	mutableImage = "ghcr.io/org/app:v1.2.3"
)

func warnsContain(t *testing.T, w []string, sub string) bool {
	t.Helper()
	for _, s := range w {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func containerServer(name, image string) *mcpv1alpha1.MCPServer {
	p := newProvider(name, mcpv1alpha1.MCPServerModeContainer)
	p.Spec.Image = image
	return p
}

func TestImageDigest_PinnedImageAlwaysAdmitted(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("block"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()

	v := &webhook.MCPServerValidator{}
	w, err := v.ValidateCreate(context.Background(), containerServer("pinned", pinnedImage))
	require.NoError(t, err)
	assert.Empty(t, w, "a digest-pinned image should produce no findings")
}

func TestImageDigest_WarnPolicyAdmitsWithWarning(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("warn"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()
	v := &webhook.MCPServerValidator{}
	w, err := v.ValidateCreate(context.Background(), containerServer("mutable-warn", mutableImage))
	require.NoError(t, err, "warn policy must not reject")
	assert.True(t, warnsContain(t, w, "not digest-pinned"), "expected a not-digest-pinned warning")
}

func TestImageDigest_BlockPolicyRejects(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("block"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()

	v := &webhook.MCPServerValidator{}
	_, err := v.ValidateCreate(context.Background(), containerServer("mutable-block", mutableImage))
	require.Error(t, err, "block policy must reject a mutable tag")
	assert.Contains(t, err.Error(), "not digest-pinned")
}

func TestImageDigest_OffPolicyIgnores(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("off"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()

	v := &webhook.MCPServerValidator{}
	w, err := v.ValidateCreate(context.Background(), containerServer("mutable-off", mutableImage))
	require.NoError(t, err)
	assert.False(t, warnsContain(t, w, "not digest-pinned"), "off policy must be silent")
}

func TestImageDigest_AnnotationOptsOutUnderBlock(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("block"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()

	p := containerServer("mutable-annotated", mutableImage)
	p.Annotations = map[string]string{"hangar.io/allow-mutable-image": "true"}
	v := &webhook.MCPServerValidator{}
	_, err := v.ValidateCreate(context.Background(), p)
	assert.NoError(t, err, "the allow-mutable-image annotation must bypass block")
}

func TestImageDigest_V1alpha2ValidatorEnforces(t *testing.T) {
	require.NoError(t, webhook.SetImageDigestPolicy("block"))
	defer func() { _ = webhook.SetImageDigestPolicy("off") }()

	p := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "v2-mutable", Namespace: "default"},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			Image: mutableImage,
		},
	}
	v := &webhook.MCPServerV1alpha2Validator{}
	_, err := v.ValidateCreate(context.Background(), p)
	require.Error(t, err, "v1alpha2 validator must also enforce image pinning")
	assert.Contains(t, err.Error(), "not digest-pinned")
}

func TestSetImageDigestPolicy_Invalid(t *testing.T) {
	require.Error(t, webhook.SetImageDigestPolicy("nonsense"))
	_ = webhook.SetImageDigestPolicy("off")
}

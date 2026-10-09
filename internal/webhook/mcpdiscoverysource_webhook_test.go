package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

// ── MCPDiscoverySource cross-namespace configMapRef (#234) ────────────

func configMapSource(ns, cmNamespace string) *mcpv1alpha2.MCPDiscoverySource {
	return &mcpv1alpha2.MCPDiscoverySource{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: ns},
		Spec: mcpv1alpha2.MCPDiscoverySourceSpec{
			Type:         mcpv1alpha2.DiscoveryTypeConfigMap,
			ConfigMapRef: &mcpv1alpha2.ConfigMapReference{Name: "providers", Namespace: cmNamespace},
		},
	}
}

func TestDiscoveryV2_CrossNamespaceConfigMapRefRejected(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	_, err := v.ValidateCreate(context.Background(), configMapSource("team-a", "team-b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.configMapRef.namespace")
	assert.Contains(t, err.Error(), `"team-a"`)
	assert.Contains(t, err.Error(), `"team-b"`)
}

func TestDiscoveryV2_SameNamespaceConfigMapRefAllowed(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	for _, cmNamespace := range []string{"", "team-a"} {
		warnings, err := v.ValidateCreate(context.Background(), configMapSource("team-a", cmNamespace))
		assert.NoError(t, err, "configMapRef.namespace %q", cmNamespace)
		assert.Empty(t, warnings)
	}
}

func TestDiscoveryV2_UpdateIntroducingCrossNamespaceRejected(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	_, err := v.ValidateUpdate(context.Background(), configMapSource("team-a", ""), configMapSource("team-a", "team-b"))
	require.Error(t, err)

	_, err = v.ValidateUpdate(context.Background(), configMapSource("team-a", "team-c"), configMapSource("team-a", "team-b"))
	require.Error(t, err, "changing one cross-namespace reference to another is not grandfathered")
}

// A source stored before the rule must stay updatable -- labels, annotations,
// the controller's finalizer -- or it could never be deleted. The controller
// still refuses to sync it.
func TestDiscoveryV2_UnchangedCrossNamespaceUpdateWarns(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	oldObj := configMapSource("team-a", "team-b")
	newObj := configMapSource("team-a", "team-b")
	newObj.Annotations = map[string]string{"example.com/note": "x"}

	warnings, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "team-b")
}

func TestDiscoveryV2_DeletingCrossNamespaceUpdateAllowed(t *testing.T) {
	v := &webhook.MCPDiscoverySourceV1alpha2Validator{}
	oldObj := configMapSource("team-a", "team-b")
	oldObj.Finalizers = []string{"mcp-hangar.io/discovery-finalizer"}
	newObj := configMapSource("team-a", "team-b")
	now := metav1.Now()
	newObj.DeletionTimestamp = &now

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	require.NoError(t, err)
}

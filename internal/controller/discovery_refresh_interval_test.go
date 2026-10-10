package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func discoverySourceWithInterval(name string, interval any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"type": "Namespace", "refreshInterval": interval,
	}}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mcp-hangar.io", Version: "v1alpha2", Kind: "MCPDiscoverySource"})
	u.SetName(name)
	u.SetNamespace("default")
	return u
}

// spec.refreshInterval is a plain string in the schema, so the apiserver
// stored "banana" and the operator then could not decode the object (#243).
func TestCRDRules_DiscoveryRefreshIntervalMustParse(t *testing.T) {
	requireRejected(t, createAndCleanup(t, discoverySourceWithInterval("cel-ri-garbage", "banana")),
		"must be a non-negative duration")
	requireRejected(t, createAndCleanup(t, discoverySourceWithInterval("cel-ri-negative", "-1m")),
		"must be a non-negative duration")
	require.NoError(t, createAndCleanup(t, discoverySourceWithInterval("cel-ri-good", "90s")))
}

package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `kubectl apply -f install.yaml` stores each object, as compact JSON, in the
// kubectl.kubernetes.io/last-applied-configuration annotation, and the
// apiserver caps an object's annotations at 256 KiB. The YAML of mcpservers and
// mcpdiscoverysources is past 256 KB, which is what #198 measured, but the
// annotation holds the JSON: about 153 KB each in 2026-10, accepted by the
// apiserver. This keeps it that way as the embedded corev1 types grow: past
// 230 KB, decide between dropping descriptions (crd:maxDescLen) and
// requiring server-side apply, before an install breaks.
func TestCRDs_FitTheClientSideApplyAnnotation(t *testing.T) {
	const limit = 230 * 1000

	crds, err := loadTestCRDs()
	require.NoError(t, err)
	for _, crd := range crds {
		data, err := json.Marshal(crd)
		require.NoError(t, err)
		assert.Less(t, len(data), limit,
			"%s is %d bytes as JSON; client-side apply needs it well under 256 KiB", crd.Name, len(data))
	}
}

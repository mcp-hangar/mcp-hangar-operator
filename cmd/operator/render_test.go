package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// ADR-031 T30: the Deployment a kbs-attested MCPServer renders is pinned. A
// change to it changes what Kata's genpolicy must be run against, so every
// kbs-attested user has to regenerate their agent policy: a change to the
// golden file ships with an upgrade note.
func TestRender_AKBSAttestedServerRendersTheGoldenDeployment(t *testing.T) {
	in, err := os.Open(filepath.Join("testdata", "kbs-attested-mcpserver.yaml"))
	require.NoError(t, err)
	defer func() { _ = in.Close() }()

	var out bytes.Buffer
	require.NoError(t, render(in, &out))

	golden := filepath.Join("testdata", "kbs-attested-deployment.golden.yaml")
	if *update {
		require.NoError(t, os.WriteFile(golden, out.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), out.String(),
		"the rendering changed: regenerate with -update, and add an upgrade note telling kbs-attested users to rerun genpolicy")
}

func TestRender_RefusesWhatItCannotRender(t *testing.T) {
	err := render(strings.NewReader("apiVersion: mcp-hangar.io/v1alpha2\nkind: MCPServer\nmetadata: {name: r}\nspec: {mode: remote, endpoint: https://x}\n"), &bytes.Buffer{})
	require.ErrorContains(t, err, "only a container-mode MCPServer")

	err = render(strings.NewReader("spec: {mode: container, imagee: typo}\n"), &bytes.Buffer{})
	require.ErrorContains(t, err, "reading the MCPServer", "an unknown field is refused, not silently dropped")
}

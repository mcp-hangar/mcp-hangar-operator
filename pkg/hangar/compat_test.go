package hangar

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoreVersionSupported(t *testing.T) {
	for reported, want := range map[string]bool{
		"2.26.0":       false,
		"2.26.9":       false,
		"0.0.0.dev":    false, // core's own fallback when its version is unknown
		MinCoreVersion: true,
		"2.27.0.dev3":  true,
		"2.36.1":       true,
		"3.0.0":        true,
	} {
		got, err := CoreVersionSupported(reported)
		require.NoError(t, err, reported)
		assert.Equal(t, want, got, reported)
	}
	for _, reported := range []string{"", "dev", "latest"} {
		_, err := CoreVersionSupported(reported)
		assert.Error(t, err, "%q is not taken for a version", reported)
	}
}

// supported reads one version out of a file and checks it meets the minimum.
func supported(t *testing.T, file string, pattern *regexp.Regexp) {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	m := pattern.FindSubmatch(raw)
	require.NotNil(t, m, "no version in %s", file)
	ok, err := CoreVersionSupported(string(m[1]))
	require.NoError(t, err)
	assert.True(t, ok, "%s carries core %s, below MinCoreVersion %s", file, m[1], MinCoreVersion)
}

// The mcp-hangar chart installs a core this operator accepts: a minimum above
// its appVersion would make the published charts refuse each other (#330).
func TestMinCoreVersion_TheCoreChartMeetsIt(t *testing.T) {
	supported(t, "testdata/mcp-hangar-chart.yaml", regexp.MustCompile(`(?m)^appVersion:\s*"?([^"\s]+)"?`))
}

// The remote e2e runs its lifecycle against a core that meets the minimum, and
// its refusal case against one that does not.
func TestMinCoreVersion_TheE2ECoresStraddleIt(t *testing.T) {
	image := regexp.MustCompile(`image:\s*ghcr\.io/mcp-hangar/mcp-hangar:(\S+)`)
	supported(t, "../../test/e2e/remote/manifests/core.yaml", image)

	raw, err := os.ReadFile("../../test/e2e/remote/manifests/core-old.yaml")
	require.NoError(t, err)
	m := image.FindSubmatch(raw)
	require.NotNil(t, m)
	ok, err := CoreVersionSupported(string(m[1]))
	require.NoError(t, err)
	assert.False(t, ok, "the refusal e2e needs a core older than %s, not %s", MinCoreVersion, m[1])
}

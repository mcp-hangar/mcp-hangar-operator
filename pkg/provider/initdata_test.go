package provider

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ADR-031 T29: the guest gets spec.initData byte for byte, and the status
// names the digests of exactly those bytes.

const initData = "version = \"0.1.0\"\nalgorithm = \"sha256\"\n[data]\n\"key_ref\" = \"payments-tls\"\n"

func decode(t *testing.T, annotation string) string {
	t.Helper()
	compressed, err := base64.StdEncoding.DecodeString(annotation)
	require.NoError(t, err)
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(raw)
}

func TestInitData_DecodesToTheSameBytes(t *testing.T) {
	for _, doc := range []string{initData, initData + "\n", "no trailing newline", "\ttabs  and  spaces \r\n"} {
		assert.Equal(t, doc, decode(t, EncodeInitData(doc)))
	}
}

func TestInitData_EncodesTheSameWayEveryTime(t *testing.T) {
	assert.Equal(t, EncodeInitData(initData), EncodeInitData(initData), "a reconcile must not roll the pod for nothing")
}

func TestInitData_DigestsAreOfTheBytes(t *testing.T) {
	s256, s384, s512 := sha256.Sum256([]byte(initData)), sha512.Sum384([]byte(initData)), sha512.Sum512([]byte(initData))
	got := InitDataDigests(initData)
	require.NotNil(t, got)
	assert.Equal(t, hex.EncodeToString(s256[:]), got.SHA256)
	assert.Equal(t, hex.EncodeToString(s384[:]), got.SHA384)
	assert.Equal(t, hex.EncodeToString(s512[:]), got.SHA512)
	assert.NotEqual(t, got.SHA256, InitDataDigests(initData+"\n").SHA256, "a trailing newline is a different document")
	assert.Nil(t, InitDataDigests(""))
}

func TestInitData_RidesOnThePodTemplate(t *testing.T) {
	srv := deploymentServer()
	dep, err := BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)
	assert.NotContains(t, dep.Spec.Template.Annotations, AnnotationKataInitData)

	srv.Spec.InitData = initData
	dep, err = BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)
	assert.Equal(t, initData, decode(t, dep.Spec.Template.Annotations[AnnotationKataInitData]))

	srv.Spec.InitData = initData + "# changed\n"
	changed, err := BuildDeploymentForMCPServer(srv)
	require.NoError(t, err)
	assert.NotEqual(t, dep.Spec.Template.Annotations[AnnotationKataInitData], changed.Spec.Template.Annotations[AnnotationKataInitData],
		"a changed document changes the template, which rolls the pod")
}

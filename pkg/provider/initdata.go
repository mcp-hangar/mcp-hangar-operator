package provider

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// AnnotationKataInitData carries a confidential guest's init-data to Kata,
// which measures it at launch (ADR-031 §3).
const AnnotationKataInitData = "io.katacontainers.config.hypervisor.cc_init_data"

// EncodeInitData is spec.initData in the form Kata reads from the annotation:
// gzip-compressed, then base64-encoded. The document's bytes are not touched,
// so the hash the Key Broker Service binds is the hash the guest presents. The
// gzip header carries no name or time, so the same document always encodes the
// same way and a reconcile never rolls the pod for nothing.
func EncodeInitData(document string) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	// Writes to a bytes.Buffer do not fail.
	_, _ = zw.Write([]byte(document))
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// InitDataDigests are the digests of the init-data's bytes, so a provisioner
// can check the hash it binds against the one the guest will present: SHA-256
// for SNP HOSTDATA, SHA-384 for TDX MRCONFIGID. Nil without init-data. The
// document is hashed, never parsed.
func InitDataDigests(document string) *mcpv1alpha2.InitDataDigests {
	if document == "" {
		return nil
	}
	s256 := sha256.Sum256([]byte(document))
	s384 := sha512.Sum384([]byte(document))
	s512 := sha512.Sum512([]byte(document))
	return &mcpv1alpha2.InitDataDigests{
		SHA256: hex.EncodeToString(s256[:]),
		SHA384: hex.EncodeToString(s384[:]),
		SHA512: hex.EncodeToString(s512[:]),
	}
}

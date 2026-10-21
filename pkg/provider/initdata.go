package provider

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// AnnotationKataInitData carries a confidential guest's init-data to Kata,
// which measures it at launch (ADR-031 §3).
const AnnotationKataInitData = "io.katacontainers.config.hypervisor.cc_init_data"

// EncodeInitData is spec.initData in the form Kata reads from the annotation:
// gzip, then base64. The document's bytes are not touched, so the hash the Key
// Broker Service binds is the hash the guest presents.
//
// The gzip stream is written here, not by compress/gzip: what that package
// emits for the same input differs between Go releases, at every level (1.26
// and 1.27 end a stored stream differently), and a changed annotation rolls
// every kbs-attested pod when the operator is rebuilt. A fixed header with no
// name or time, stored deflate blocks (RFC 1951 §3.2.4) and the CRC-32 and size
// trailer (RFC 1952) encode one document one way. The cost is size, which
// spec.initData's maxLength bounds.
func EncodeInitData(document string) string {
	data := []byte(document)
	var buf bytes.Buffer
	// ID1 ID2, CM=deflate, no flags, MTIME 0, XFL 0, OS unknown.
	buf.Write([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff})
	for first := true; first || len(data) > 0; first = false {
		n := min(len(data), 0xffff)
		final := byte(0)
		if n == len(data) {
			final = 1
		}
		buf.WriteByte(final) // BFINAL, BTYPE 00 (stored)
		_ = binary.Write(&buf, binary.LittleEndian, uint16(n))
		_ = binary.Write(&buf, binary.LittleEndian, ^uint16(n))
		buf.Write(data[:n])
		data = data[n:]
	}
	_ = binary.Write(&buf, binary.LittleEndian, crc32.ChecksumIEEE([]byte(document)))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(document)))
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

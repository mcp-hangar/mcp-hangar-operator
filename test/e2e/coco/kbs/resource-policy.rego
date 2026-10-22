# The KBS resource policy for T26 (mcp-hangar/mcp-hangar#1950, ADR-031).
#
# TEST ONLY. It admits Trustee's sample attester, whose evidence is unsigned:
# any client can claim any init-data hash. A run under this policy proves the
# wiring -- provisioning, the init-data binding, the pinned SPKI -- and no
# confidentiality. A production policy must also require hardware evidence and
# an affirming appraisal.
#
# up.sh replaces the placeholder with the operator's status.initDataDigests.sha256
# of the MCPServer, re-encoded as base64: the sample verifier puts the hash in the
# init_data claim as base64 of the digest bytes.
package policy

import rego.v1

default allow := false

key_path := ["default", "hangar-e2e", "tls-key"]

bound_init_data := "@INIT_DATA_SHA256_B64@"

allow if {
	data.plugin == "resource"
	data["resource-path"] == key_path
	input.submods.cpu0["ear.veraison.annotated-evidence"].init_data == bound_init_data
}

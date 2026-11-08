package hangar

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/version"
)

// MinCoreVersion is the oldest core this operator works with (#330). Every
// MCPServer it registers is a private endpoint (a pod IP since #334, an
// in-cluster Service for most remote servers), and core accepts one only from
// ssrf.trusted_networks, which core 2.27.0 introduced (core #1753). An older
// core refuses every such registration.
//
// Features newer than this are checked per feature, not here: a key-pinned
// server needs core to list peer_identity in GET /api/system/, and a core
// without provenance is judged by the Deployment alone.
//
// A test holds this at or below the core appVersion the mcp-hangar chart ships
// and the core image the remote e2e runs.
const MinCoreVersion = "2.27.0"

// CoreVersionSupported reports whether a version core reported is at least
// MinCoreVersion. Core reports a PEP 440 version; the numeric release is
// compared and anything after it (".dev3", "rc1") is ignored. A version that
// does not start with a numeric release is an error, not "older".
func CoreVersionSupported(reported string) (bool, error) {
	v, err := version.ParseGeneric(reported)
	if err != nil {
		return false, fmt.Errorf("core reported version %q, which is not a release version: %w", reported, err)
	}
	return v.AtLeast(version.MustParseGeneric(MinCoreVersion)), nil
}

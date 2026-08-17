package webhook

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// Registration binds one served API version of one kind to its admission
// validator. main.go registers exactly this list with the manager, and the
// registration-parity test asserts the list covers every served CRD version
// -- so a new API version cannot ship without a validator wired for it.
//
// Why this exists: writes submitted at a served version only ever reach THAT
// version's validator (issue #12), and the wildcard-egress guard survived for
// months only in the v1alpha1 validator -- unserved meant unenforced, with
// green tests against the dead code path (#137). The list lived inline in
// main.go then, where no test could see it.
type Registration struct {
	// Name is "<Kind>/<version>", e.g. "MCPServer/v1alpha2".
	Name      string
	Object    runtime.Object
	Validator admission.CustomValidator
}

// Registrations returns every validating-webhook registration the operator
// wires. Order is not significant.
func Registrations() []Registration {
	return []Registration{
		{"MCPServer/v1alpha2", &mcpv1alpha2.MCPServer{}, &MCPServerV1alpha2Validator{}},
		{"MCPServerGroup/v1alpha2", &mcpv1alpha2.MCPServerGroup{}, &MCPServerGroupV1alpha2Validator{}},
		{"MCPDiscoverySource/v1alpha2", &mcpv1alpha2.MCPDiscoverySource{}, &MCPDiscoverySourceV1alpha2Validator{}},
	}
}

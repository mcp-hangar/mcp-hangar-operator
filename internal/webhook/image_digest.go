package webhook

import "strings"

// allowMutableImageAnnotation opts a single MCPServer out of image-digest
// enforcement (parallels hangar.io/allow-unrestricted-egress for wildcard egress).
const allowMutableImageAnnotation = "hangar.io/allow-mutable-image"

// IsImageDigestPinned reports whether a container image is pinned by digest
// (image@sha256:...) or the workload opts out of pinning via the
// allow-mutable-image annotation. An empty image is treated as pinned (no
// mutable tag to enforce). The controller's egress pin coupling uses it
// (#52/#51); the image-digest ValidatingAdmissionPolicies in
// config/admissionpolicy mirror it, and internal/admissionpolicy tests that
// they agree.
func IsImageDigestPinned(image string, annotations map[string]string) bool {
	if image == "" || strings.Contains(image, "@sha256:") {
		return true
	}
	return annotations[allowMutableImageAnnotation] == "true"
}

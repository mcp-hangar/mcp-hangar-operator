package webhook

import (
	"fmt"
	"strings"
)

// allowMutableImageAnnotation opts a single MCPServer out of image-digest
// enforcement (parallels hangar.io/allow-unrestricted-egress for wildcard egress).
const allowMutableImageAnnotation = "hangar.io/allow-mutable-image"

// imageDigestPolicy controls how container images that are not pinned by digest
// are handled at admission: "block" rejects, "warn" (default) emits an admission
// warning, "off" ignores. Set once at operator startup via SetImageDigestPolicy.
var imageDigestPolicy = "warn"

// SetImageDigestPolicy sets the image-digest enforcement policy. Valid values are
// "off", "warn", "block".
func SetImageDigestPolicy(p string) error {
	switch p {
	case "off", "warn", "block":
		imageDigestPolicy = p
		return nil
	default:
		return fmt.Errorf("invalid image-digest-policy %q (want off|warn|block)", p)
	}
}

// checkImageDigest evaluates a container image against imageDigestPolicy. It
// returns a hard error message and/or a warning message; both empty means the
// image is fine (already digest-pinned, empty, opted out, or policy "off").
//
// "Digest-pinned" means the reference carries an @sha256:... digest, e.g.
// ghcr.io/org/app@sha256:... or ghcr.io/org/app:tag@sha256:... -- a mutable tag
// alone can be re-pointed after admission, defeating reproducible/verifiable deploys.
func checkImageDigest(image string, annotations map[string]string) (errMsg, warnMsg string) {
	if image == "" || strings.Contains(image, "@sha256:") {
		return "", ""
	}
	if annotations[allowMutableImageAnnotation] == "true" {
		return "", ""
	}
	switch imageDigestPolicy {
	case "block":
		return fmt.Sprintf(
			"spec.image %q is not digest-pinned; use an image@sha256:... reference (or set annotation %s: \"true\" to allow a mutable tag)",
			image, allowMutableImageAnnotation), ""
	case "warn":
		return "", fmt.Sprintf(
			"spec.image %q is not digest-pinned; a mutable tag can change under you after admission. Pin by digest (image@sha256:...) for reproducible, verifiable deploys.",
			image)
	}
	return "", ""
}

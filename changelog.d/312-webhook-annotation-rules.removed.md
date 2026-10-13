**webhook:** remove the MCPServer webhook's copies of the wildcard egress
opt-in and the image digest policy; the ValidatingAdmissionPolicies in
`config/admissionpolicy/` are the only enforcement. `--webhook-annotation-rules`
and `--image-digest-policy` are accepted and ignored, with a log line when set,
and go in a later release

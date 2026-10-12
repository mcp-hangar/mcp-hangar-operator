**webhook:** ship the wildcard egress opt-in and the image digest policy as
ValidatingAdmissionPolicies in `config/admissionpolicy/`, so the apiserver
enforces them with the operator's webhooks off. The digest level is the
`imageDigestPolicy` key of a params ConfigMap (`warn`, `block`, `off`). The
webhook copies stay behind `--webhook-annotation-rules` (default true) for this
release; `config/default` installs the policies and sets it false

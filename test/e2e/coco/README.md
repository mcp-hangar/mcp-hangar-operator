# kbs-attested end to end (T26)

The harness for acceptance test T26 of ADR-030 as ADR-031 runs it
(mcp-hangar/mcp-hangar#1950). A `kbs-attested` MCPServer runs on
`kata-qemu-coco-dev`. A Trustee KBS releases its TLS key only to a guest whose
init-data hash is the one the operator publishes in `status.initDataDigests`.
Core pins the key's SPKI.

## What a run proves

It proves the wiring, not confidentiality. The guest attests with Trustee's
sample attester, whose evidence is unsigned. Any client can claim any init-data
hash, and `kbs/resource-policy.rego` admits sample evidence, which no
production policy may do. Keep this policy and every key it generates out of
the samples and the chart.

## What is here

- `upstream/`: an MCP server with one `echo` tool. At start it asks the
  Confidential Data Hub at `127.0.0.1:8006` for `KBS_RESOURCE` and serves TLS
  with that key and a self-signed certificate. It holds both only in memfds and
  exits non-zero if no key is released.
- `kbs/`: the resource policy and its tests.
- `check.sh` (`make e2e-coco-check`, CI on changes here): the policy tests,
  plus the image against a stub CDH. The SPKI a client sees must equal the pin
  computed from the key, and with no key there is no server.

## The pin

Compute the pin from the key as the certificate encodes it. A P-256 key
generated with explicit curve parameters, which LibreSSL produces by default,
has a different SPKI from the certificate built from it. Generate keys with
`-pkeyopt ec_param_enc:named_curve`.

## Not here yet

The cluster leg: kind with `/dev/kvm` passed into the node, `kata-qemu-coco-dev`,
Trustee with this policy, the provisioning of the key, the agent policy from
`manager render`, the Go test and its workflow. It waits on a runner with KVM.

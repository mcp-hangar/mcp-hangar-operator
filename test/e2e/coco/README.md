# kbs-attested end to end (T26)

This harness runs acceptance test T26 of ADR-030 as ADR-031 runs it
(mcp-hangar/mcp-hangar#1950).

- A `kbs-attested` MCPServer runs on `kata-qemu-coco-dev`.
- A Trustee KBS releases its TLS key only to a guest whose init-data hash is
  the one the CR's init-data has.
- Core pins the key's SPKI.

## What a run proves

It proves the wiring, not confidentiality. The guest attests with Trustee's
sample attester, whose evidence is unsigned, and `kbs/resource-policy.rego`
admits it. No production policy may do that. An honest guest reports the hash
of the init-data it was given, which is what the refusal of the second pod
shows; a dishonest one could claim any hash.

Keep this policy, and every key the test generates, out of the samples and the
chart.

## What a run asserts

`coco_test.go` acts as the provisioner:

1. It generates a P-256 key and a self-signed certificate, and keeps the SPKI
   as the CR's pin.
2. Through the KBS admin API, with a JWT signed by the admin key `up.sh`
   generated, it uploads the bundle of key and certificate, and a resource
   policy bound to the base64 SHA-256 of the CR's init-data.
3. It creates the MCPServer.

The CR's init-data also carries an agent policy. `manager render`, run from
the operator image under test, prints the Deployment, and `genpolicy` from the
same kata release as the runtime turns it into Rego.

Then it checks:

- `status.initDataDigests.sha256` is the hash the KBS binds;
- the pod gets the key, serves TLS with it, core's handshake check passes,
  and `hangar_call echo` through the gateway succeeds (T1);
- `kubectl exec` into the pod is refused by the agent policy;
- a bare pod on the same runtime with init-data that differs by one comment
  exits without serving, and the KBS log shows it asked for the key and was
  never answered 200.

## Running it

It needs a host with `/dev/kvm`. GitHub-hosted `ubuntu-latest` runners have
one, and the `e2e coco` workflow runs there nightly and on PRs that touch this
directory.

```bash
make e2e-coco-up     # kind, kata-deploy, KBS, core behind TLS, operator
make e2e-coco        # the Go test
make e2e-coco-clean
```

`make e2e-coco-check` runs the parts that need no KVM, with Docker only:

- the policy's OPA tests;
- the upstream against a stub CDH.

## What is here

- `upstream/server.py`: a stdlib-only MCP server with one `echo` tool. It runs
  as `python -c` on a public Python image pinned by digest, because the guest
  pulls its image itself and cannot reach a registry local to kind. At start it
  asks the Confidential Data Hub at `127.0.0.1:8006` for `KBS_RESOURCE`, holds
  the bundle in a memfd, and exits non-zero if nothing is released.
- `kbs/`: the resource policy and its tests.
- `manifests/kbs.yaml`: Trustee v0.22.0's KBS with the built-in AS. Its
  Service has a fixed ClusterIP, because the init-data names that address and
  the key is bound to the init-data's hash.
- `manifests/core.yaml`: core 2.33.0 with SQLite. A peer identity needs a
  durable configuration store, and core refuses the registration without one.
  A socat sidecar terminates TLS, because the operator sends a key list only
  over https.
- `operator/`: the operator from this checkout, pointed at core with `up.sh`'s CA.
- `kata-values.yaml`: kata-deploy 4.2.0 with only `qemu-coco-dev`, with
  `cc_init_data` allowed. The default shim must be one that is enabled.

## Traps

- **The pin.** Compute it from the key as the certificate encodes it. A P-256
  key with explicit curve parameters, which LibreSSL generates by default, has
  a different SPKI from its certificate. Use `ec_param_enc:named_curve`, or Go's
  `x509`, as the test does.
- **The hash.** The sample verifier writes the `init_data` claim as base64 of
  the digest bytes. `status.initDataDigests` is hex.

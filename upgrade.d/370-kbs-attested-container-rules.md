### A kbs-attested container server is held to its init-data

A container-mode MCPServer whose `spec.tls.peerIdentity.issuance` is `kbs-attested` must now satisfy all of the following (ADR-031):
- set `spec.initData`, naming `spec.tls.peerIdentity.keyRef` in it;
- reference `spec.image` by digest (`image@sha256:...`);
- take no `spec.env` value from a Secret;
- use only `emptyDir` volumes;
- set `spec.automountServiceAccountToken: false`.

Such a CR was never registered with core, so no running server changes. An existing one is refused on its next change until it meets the rules: the apiserver ratchets unchanged fields on update, so an update that leaves them alone may still pass, depending on your Kubernetes version. `spec.initData` is refused on every other server.

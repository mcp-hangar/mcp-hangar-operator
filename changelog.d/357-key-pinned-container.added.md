**controller:** a container-mode `MCPServer` with `spec.tls.peerIdentity` now runs (ADR-030 §4a, §4c, §4f).
- **Registration:** it is registered with core with its key list before its pod exists, without an endpoint, and core is not asked to start it until it has one.
- **Endpoint:** it follows the pod. It is set to `https://<pod IP>:8080` (IPv6 bracketed) once the pod has an address, and cleared while no pod has one, so a call is refused rather than sent to an address another pod may inherit.
- **Discovery:** its pod carries no discovery annotations, so core never discovers it as a plain server.
- **Moving a discovered server:** a server that core discovered from this MCPServer's own pod before the key was declared is updated in place, not registered again. A server under the same name that someone else registered is reported as `NameTaken` and left alone.
- **Refused registrations:** when a key-pinned CR cannot be registered (no capability, plaintext `hangar.url`, or a declared `issuance`), anything it registered before is removed from core rather than left serving the name plain.

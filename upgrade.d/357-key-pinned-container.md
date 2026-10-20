### Key-pinned container servers are reached over https on the pod's address

A container-mode `MCPServer` with `spec.tls.peerIdentity` is no longer found by core's discovery. The operator registers it, and core reaches it at `https://<pod IP>:8080`. Three things follow:

- **TLS on port 8080:** the server must serve TLS on port 8080, with a key whose SPKI digest is listed in `spkiSha256`.
- **SSRF policy:** core must allow the pod network under its SSRF policy, through `ssrf.trusted_networks`.
- **Moving an existing server:** a server core already discovered from such a pod is moved to the key-pinned registration the first time the operator reconciles the CR after the key is declared. Until the moved server's pod has an address, its endpoint is empty and calls to it are refused.

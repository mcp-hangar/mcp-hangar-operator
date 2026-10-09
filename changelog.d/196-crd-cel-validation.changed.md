**api:** the apiserver now enforces the MCPServer, MCPDiscoverySource and
MCPEgressPolicy rules that used to live only in the validating webhooks, which
are off by default: a container-mode MCPServer needs an `image`, a remote-mode
one an absolute `http`/`https` `endpoint` with a host, `startupTimeout` and
`shutdownGracePeriod` must be non-negative durations, `expectedTools` entries
must be non-empty and unique, an egress `cidr` must be a well-formed CIDR, and a
`ConfigMap` discovery source needs a `configMapRef`. `MCPServer.spec.mode` and
`MCPEgressPolicy.spec.targetRef` are now immutable. The webhook keeps only the
checks the schema cannot express (annotation opt-ins, the cross-namespace
ConfigMap reference, filter regexps) and its warnings; see UPGRADE.md

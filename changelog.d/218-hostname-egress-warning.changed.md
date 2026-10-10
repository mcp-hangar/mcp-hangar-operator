**webhook:** the admission warning for a host-only egress rule now says to
add a `cidr` or allow the hostname with an MCPEgressPolicy using the Cilium
flavor, instead of pointing at a Tetragon backend that does not exist

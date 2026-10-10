### `cidr: 0.0.0.0/0` and `::/0` need the unrestricted-egress annotation

With the validating webhook enabled, an MCPServer whose egress rule has a
zero-length CIDR (`0.0.0.0/0`, `::/0`) is refused on create and update unless
it carries `hangar.io/allow-unrestricted-egress: "true"`, the opt-in that
`host: "*"` already required. These rules open every destination, which the
old gate did not see. Add the annotation to keep such a server as it is, or
narrow the CIDR. With the annotation, the operator emits a Warning Event
`UnrestrictedEgressAllowed`, as it does for `host: "*"`.

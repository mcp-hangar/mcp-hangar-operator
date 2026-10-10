**webhook:** an egress rule with `cidr: 0.0.0.0/0` or `::/0` now needs the
`hangar.io/allow-unrestricted-egress: "true"` annotation, and its use is
audited with `UnrestrictedEgressAllowed`, as `host: "*"` already was. The CIDR
forms opened every destination with neither; see UPGRADE.md

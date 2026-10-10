**controller:** discovery refuses a generated MCPServer name Kubernetes cannot
use (uppercase, `/`, longer than 63 characters) per entry, with the reason in
`status.discoveredProviders`, instead of failing at Create; and deleting an
`Additive` source with `ownership.controller: false` leaves its servers in
place instead of deleting them. See UPGRADE.md

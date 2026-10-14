### The operator's core key needs `admin` to register servers and deliver policy

This corrects the 0.17.26 note, which said `developer` and `admin` hold what the operator needs. With auth on, the operator's API key needs `mcp_servers:write`, `mcp_servers:lifecycle` and `policy:write`. In core's built-in roles only `admin` holds all three: `developer` has the first two and the L7 policy push gets `403`, and `provider-admin` has the last and registration gets `403` (`RegistrationForbidden`).

Until core ships a narrower role, use an `admin` key if the operator registers remote servers. An operator that only delivers policy still works with `provider-admin`.

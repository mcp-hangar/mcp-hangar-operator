**infra:** `/metrics` is served over HTTPS and requires a bearer token the API
server authenticates and authorizes for `get` on the `/metrics` non-resource
URL. Served plain, any pod in the cluster could read server names, states, tool
counts and reconcile errors. `--metrics-secure=false` restores plain HTTP; see
UPGRADE.md

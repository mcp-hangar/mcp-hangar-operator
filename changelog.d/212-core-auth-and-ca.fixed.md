**controller:** a remote MCPServer whose core rejects the operator's
credentials (401/403) reads `Degraded/CoreAuthRejected` with one Warning,
re-checked at the steady interval, instead of `HealthCheckFailed` every 10 s
with a Warning each time, indistinguishable from an outage.
`--hangar-ca-file` and `--hangar-tls-server-name` let the operator reach an
https core whose certificate a private CA signed

### `type: Namespace` discovery sources need a template endpoint

A namespace names no Service, so the remote MCPServers a `type: Namespace` source creates had no endpoint and the apiserver refused every one of them. The CRD now refuses the source instead: set `spec.providerTemplate.spec.endpoint` (and `mode: remote`) on it.

A source already stored without the field is not deleted. It reports the missing endpoint in its status and creates nothing, as it did before. Add the field to make it work.

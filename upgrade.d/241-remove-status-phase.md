### `MCPServer.status.phase` is removed

The field was declared but never written, so it was always empty. Read
`status.state` or the `Ready` condition instead. Once the new CRD is applied the
apiserver prunes any value stored in it, so nothing needs migrating.

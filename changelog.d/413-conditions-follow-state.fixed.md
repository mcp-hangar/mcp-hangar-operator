**controller:** an MCPServer's conditions now agree with its `State` in the
three places that still disagreed. A remote server that recovers clears the
`Degraded=True` its failing branch wrote, so an alert on it stops firing. A
container server that leaves Ready (containers starting, a tools read that
fails, a pod pending, recreated or stuck) reports `Available=False` and no
ready or available replicas, where it kept those of its last Ready. A server
held because core is incompatible reads `State=Initializing` beside its
`Ready=False`, not the `State` it had before

**controller:** one slow or unreachable core no longer stalls every MCPServer.
A core call now gives up after 5 s with its retries included (it used to hold
a reconcile for about 43 s on a core that accepted connections and never
answered), and the MCPServer and MCPEgressPolicy controllers run 4 reconciles
at once (`--max-concurrent-reconciles`), so a container server's pod is created
while remote servers wait on core

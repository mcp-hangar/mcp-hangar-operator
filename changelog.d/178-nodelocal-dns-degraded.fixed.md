**controller:** an MCPEgressPolicy with FQDN upstreams on Cilium now reports `Degraded` with reason `FQDNLookupsUnobserved` when NodeLocal DNSCache keeps Cilium's DNS proxy from seeing the lookups. The status used to say "FQDN + CIDR enforced" while every hostname upstream was denied. There are two such cases, and the message names the cache, the denied upstreams and the fix for each:

- The cache runs on the host network. Run it as a pod behind a CiliumLocalRedirectPolicy instead.
- The cache runs as a pod, but `--dns-egress-selectors` does not select its pods. Add `--dns-egress-selectors=kube-system/k8s-app=node-local-dns`.

**controller:** `--dns-egress-selectors` names resolver pods other than
`kube-system/k8s-app=kube-dns` that DNS may reach, such as
`openshift-dns/dns.operator.openshift.io/daemonset-dns=default` on OpenShift,
in the per-server policy, the namespace default-deny and both backstop
flavors. Clusters whose resolver is not kube-dns lost DNS in governed
namespaces, and `--dns-egress-cidrs` could not reach a Service ClusterIP

#!/usr/bin/env bash
# Install NodeLocal DNSCache into the e2e kind cluster the way Cilium supports it.
#
# The cache runs as an ordinary pod (not on the host network) and a
# CiliumLocalRedirectPolicy steers traffic for the kube-dns service to the
# cache pod on the same node. Pods keep the kube-dns service IP in resolv.conf;
# the redirect happens in Cilium's datapath, so its DNS proxy still observes
# every lookup and toFQDNs learns the addresses a name resolved to.
#
# The other topology -- kubelet pointed at a link-local address served by a
# cache on the host network -- is the one #178 found: the proxy never sees a
# lookup, the toFQDNs allow-list stays empty, and the allow-listed upstream is
# denied. The operator reports that as Degraded; this leg proves the topology
# it points people at.
#
# Both manifests are Cilium's own examples, pinned to the Cilium version the
# cluster runs, so an upstream edit cannot change what this leg tests without
# a commit here.
set -euo pipefail

CONTEXT="${1:?kube context}"
CILIUM_REF="${2:?cilium ref for the example manifests}"

base="https://raw.githubusercontent.com/cilium/cilium/${CILIUM_REF}/examples/kubernetes-local-redirect"
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT

curl -sSfL -o "$dir/node-local-dns.yaml" "$base/node-local-dns.yaml"
curl -sSfL -o "$dir/node-local-dns-lrp.yaml" "$base/node-local-dns-lrp.yaml"

kubedns="$(kubectl --context "$CONTEXT" get svc kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}')"
test -n "$kubedns" || { echo "kube-dns has no clusterIP; is CoreDNS installed?" >&2; exit 1; }

# The one substitution Cilium's guide documents for its example manifest.
sed -i.bak -e "s/__PILLAR__DNS__SERVER__/${kubedns}/g" "$dir/node-local-dns.yaml"

kubectl --context "$CONTEXT" apply -f "$dir/node-local-dns.yaml"
kubectl --context "$CONTEXT" -n kube-system rollout status daemonset/node-local-dns --timeout=180s
kubectl --context "$CONTEXT" apply -f "$dir/node-local-dns-lrp.yaml"

# A cache that is Running but not answering would make every DNS-dependent
# assertion fail for a reason that has nothing to do with policy, so prove a
# pod resolves through the service IP before handing the cluster to the tests.
# Whether the answer came from the cache is the first test's job.
for _ in $(seq 1 30); do
  if kubectl --context "$CONTEXT" run nodelocaldns-check \
      --image=busybox:1.36 --restart=Never --rm -i --quiet --timeout=60s \
      --command -- nslookup kubernetes.default.svc.cluster.local >/dev/null 2>&1; then
    echo "pods resolve through kube-dns (${kubedns}) with the node-local-dns redirect in place"
    exit 0
  fi
  sleep 5
done
echo "pods could not resolve through kube-dns after installing the node-local-dns redirect" >&2
exit 1

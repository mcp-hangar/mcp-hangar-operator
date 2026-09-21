#!/usr/bin/env bash
# Install NodeLocal DNSCache into the e2e kind cluster.
#
# The topology this reproduces is the one the MCPEgressPolicy epic flagged at
# design time and nothing tested (#145): pods resolve through a link-local
# address served by a per-node cache, not through the kube-dns endpoints the
# generated DNS egress rule selects. A policy whose DNS rule does not cover
# that address blocks resolution outright, and a DNS path the CNI's proxy does
# not observe cannot teach toFQDNs which IPs a name resolved to.
#
# kubelet is pointed at the link-local address by the kind config in the
# Makefile, so this uses the upstream substitution for that mode: the cache
# binds only __PILLAR__LOCAL__DNS__ and forwards misses to the kube-dns
# service IP.
set -euo pipefail

CONTEXT="${1:?kube context}"
LOCAL_DNS_IP="${2:?node-local DNS address}"
K8S_REF="${3:?kubernetes ref for the addon manifest}"
DNS_DOMAIN="${DNS_DOMAIN:-cluster.local}"

manifest="$(mktemp)"
trap 'rm -f "$manifest" "$manifest.bak"' EXIT

curl -sSfL -o "$manifest" \
  "https://raw.githubusercontent.com/kubernetes/kubernetes/${K8S_REF}/cluster/addons/dns/nodelocaldns/nodelocaldns.yaml"

kubedns="$(kubectl --context "$CONTEXT" get svc kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}')"
test -n "$kubedns" || { echo "kube-dns has no clusterIP; is CoreDNS installed?" >&2; exit 1; }

# The same substitution upstream documents for a kubelet pointed at the cache:
# the kube-dns service IP is dropped from -localip (so the cache does not also
# bind it) and becomes the upstream the cache forwards to.
sed -i.bak \
  -e "s/__PILLAR__LOCAL__DNS__/${LOCAL_DNS_IP}/g" \
  -e "s/__PILLAR__DNS__DOMAIN__/${DNS_DOMAIN}/g" \
  -e "s/,__PILLAR__DNS__SERVER__//g" \
  -e "s/__PILLAR__CLUSTER__DNS__/${kubedns}/g" \
  "$manifest"

kubectl --context "$CONTEXT" apply -f "$manifest"
kubectl --context "$CONTEXT" -n kube-system rollout status daemonset/node-local-dns --timeout=180s

# A cache that is Running but not answering would make every DNS-dependent
# assertion below fail for a reason that has nothing to do with policy, so
# prove it resolves before handing the cluster to the tests.
kubectl --context "$CONTEXT" run nodelocaldns-check \
  --image=busybox:1.36 --restart=Never --rm -i --quiet --timeout=120s \
  --command -- nslookup kubernetes.default.svc."${DNS_DOMAIN}" "${LOCAL_DNS_IP}" >/dev/null
echo "node-local-dns answers on ${LOCAL_DNS_IP}"

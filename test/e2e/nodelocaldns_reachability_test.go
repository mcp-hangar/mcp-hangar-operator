//go:build e2e

// The toFQDNs / NodeLocal DNSCache interaction, with real packets.
//
// The first version of this leg (#145) ran the upstream addon the usual way --
// kubelet pointed at a link-local address, the cache on the host network --
// and found that FQDN upstreams cannot work there under Cilium (#178): the
// destination of a lookup is the node itself, so Cilium's DNS proxy never
// observes one, the toFQDNs allow-list stays empty, and the allow-listed
// upstream is denied along with everything else. It failed closed, but the
// policy was unusable, and the DNS egress rule was not the control point.
//
// Cilium's supported answer is a different topology: the cache runs as an
// ordinary pod and a CiliumLocalRedirectPolicy steers traffic for the kube-dns
// service to the cache on the same node. That is what this leg now installs
// (test/e2e/nodelocaldns.sh), and what the operator's docs point people at;
// the link-local topology is reported by the operator as Degraded instead.
//
// What it takes from policy: nothing extra. The first run of this leg expected
// the DNS egress rule to need the cache pods named, since a redirected lookup
// lands on a node-local-dns pod; it does not -- a rule naming only kube-dns
// covers it, and the operator runs here with its defaults. What is asserted
// instead is that the rule still scopes DNS: a governed pod cannot query a
// resolver outside the cluster.
//
// This leg runs with E2E_NODELOCAL_DNS=1 (`make e2e-cluster E2E_CNI=cilium
// E2E_NODELOCAL_DNS=1`). It is gated on the environment variable rather than on
// detecting the cache: a detection-based skip would turn "the cache failed to
// install" into a green run, which is the failure this file exists to stop.
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// externalResolver is a public resolver a governed pod must not be able to
// query directly.
const externalResolver = "1.1.1.1"

func requireNodeLocalDNSLeg(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_NODELOCAL_DNS") != "1" {
		t.Skip("E2E_NODELOCAL_DNS != 1: run via `make e2e E2E_CNI=cilium E2E_NODELOCAL_DNS=1`")
	}
}

// resolves reports whether a pod can resolve a name through whatever resolver
// its own resolv.conf names. It is a separate question from canReach on
// purpose: "the allow-listed host is unreachable" has two very different
// causes, and a DNS rule that does not cover the resolver is one of them.
func resolves(t *testing.T, cs *kubernetes.Clientset, ns, name string, labels map[string]string, host string) bool {
	t.Helper()
	script := fmt.Sprintf("nslookup %s >/dev/null 2>&1 && echo RESOLVED || echo UNRESOLVED", host)
	return probeVerdict(t, cs, ns, name, labels, script, "RESOLVED", "UNRESOLVED")
}

// nodeLocalDNSPodIP returns the address of the cache pod. The e2e cluster has
// one node, so there is one.
func nodeLocalDNSPodIP(t *testing.T, cs *kubernetes.Clientset) string {
	t.Helper()
	pods, err := cs.CoreV1().Pods("kube-system").List(context.Background(),
		metav1.ListOptions{LabelSelector: "k8s-app=node-local-dns"})
	if err != nil {
		t.Fatalf("list node-local-dns pods: %v", err)
	}
	for _, p := range pods.Items {
		if p.Status.PodIP != "" {
			return p.Status.PodIP
		}
	}
	t.Fatalf("no node-local-dns pod has an IP; is the cache installed?")
	return ""
}

// TestNodeLocalDNSCacheIsTheResolver is the precondition every other assertion
// in this file rests on. If lookups are not actually reaching the cache, the
// leg is an expensive copy of the plain cilium one and would report success
// for it.
//
// Pods keep the kube-dns service IP in resolv.conf under the redirect, so the
// file proves nothing. The cache's own request counter does: without the
// redirect nothing sends it a query, so ten lookups from a pod must move it by
// at least ten.
func TestNodeLocalDNSCacheIsTheResolver(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	metrics := fmt.Sprintf("http://%s:9253/metrics", nodeLocalDNSPodIP(t, cs))
	script := fmt.Sprintf(`m() { wget -qO- %s | awk '/^coredns_dns_requests_total/ {s+=$2} END {print s+0}'; }
b=$(m); i=0
while [ $i -lt 10 ]; do nslookup "cache-check-$i.invalid" >/dev/null 2>&1; i=$((i+1)); done
a=$(m)
echo "requests before=$b after=$a"
awk -v a="$a" -v b="$b" 'BEGIN { if (a - b >= 10) print "VIA_CACHE"; else print "NOT_VIA_CACHE" }'`, metrics)
	if !probeVerdict(t, cs, ns, "resolver-check", nil, script, "VIA_CACHE", "NOT_VIA_CACHE") {
		t.Fatalf("lookups through kube-dns did not reach the node-local-dns cache -- the "+
			"CiliumLocalRedirectPolicy is not steering DNS, and every assertion on this leg would "+
			"pass for the wrong reason")
	}
}

// TestNodeLocalDNSDefaultRuleCoversTheRedirectAndStaysScoped records what the
// DNS egress rule does under the redirect. A governed pod resolves with the
// rule the operator builds by default, naming only kube-dns, so the topology
// the docs point at needs no DNS flag. And the rule still scopes DNS: the same
// pod cannot ask a resolver outside the cluster, so "it resolves" is not the
// rule having stopped mattering.
func TestNodeLocalDNSDefaultRuleCoversTheRedirectAndStaysScoped(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	direct := fmt.Sprintf("nslookup %s %s >/dev/null 2>&1 && echo RESOLVED || echo UNRESOLVED", allowedFQDN, externalResolver)
	control := map[string]string{"role": "control"}
	if !resolves(t, cs, ns, "control-resolve", control, allowedFQDN) {
		t.Fatalf("%s does not resolve with NO policy applied -- the cache is broken, and "+
			"every verdict below would be about the wrong thing", allowedFQDN)
	}
	if !probeVerdict(t, cs, ns, "control-resolve-direct", control, direct, "RESOLVED", "UNRESOLVED") {
		t.Fatalf("%s cannot be queried directly with NO policy applied, so the scoping check below "+
			"would pass for the wrong reason", externalResolver)
	}

	policy := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "nodelocal-dns", Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			Mode: mcpv1alpha2.EgressPolicyModeEnforce,
			Upstreams: []mcpv1alpha2.UpstreamRule{
				{Name: "net", Match: mcpv1alpha2.UpstreamMatch{Host: "10.0.0.0/8"}},
			},
		},
	}
	target := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: "srv"}}
	selected := map[string]string{networkpolicy.LabelProvider: "srv"}

	if len(networkpolicy.ExtraDNSSelectors) > 0 || len(networkpolicy.ExtraDNSEgressPeers) > 0 {
		t.Fatalf("the suite configured extra DNS peers; this test is about the default rule")
	}
	backstop, _ := networkpolicy.BuildEgressPolicyBackstop(policy, target)
	applyPolicy(t, cs, backstop)
	if !resolves(t, cs, ns, "probe-resolve", selected, allowedFQDN) {
		t.Errorf("a governed pod cannot resolve %s through the redirected kube-dns service with the "+
			"default DNS rule: the documented topology would need a DNS flag after all", allowedFQDN)
	}
	if probeVerdict(t, cs, ns, "probe-resolve-direct", selected, direct, "RESOLVED", "UNRESOLVED") {
		t.Errorf("a governed pod queried %s directly: the DNS rule no longer scopes port 53 (#56)",
			externalResolver)
	}
}

// TestNodeLocalDNSCiliumFQDNAllowListHolds is the claim #178 is about, on the
// topology the docs point at: with the cache in the path through the redirect,
// an allow-listed hostname connects and a name outside the list stays blocked.
//
// Both halves are asserted, because a cluster with no egress at all would
// satisfy the second on its own, and a broken proxy would satisfy neither.
func TestNodeLocalDNSCiliumFQDNAllowListHolds(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	requireCiliumLeg(t)
	cs := clientset(t)
	dyn := dynamicClient(t)
	ns := namespace(t, cs, nil)

	if !canReach(t, cs, ns, "control-allowed", map[string]string{"role": "control"}, allowedFQDN, fqdnPort) {
		t.Fatalf("%s:%d unreachable with NO policy applied", allowedFQDN, fqdnPort)
	}
	if !canReach(t, cs, ns, "control-disallowed", map[string]string{"role": "control"}, disallowedFQDN, fqdnPort) {
		t.Fatalf("%s:%d unreachable with NO policy applied", disallowedFQDN, fqdnPort)
	}

	policy := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "nodelocal-fqdn", Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			Mode: mcpv1alpha2.EgressPolicyModeEnforce,
			Upstreams: []mcpv1alpha2.UpstreamRule{
				{Match: mcpv1alpha2.UpstreamMatch{Host: allowedFQDN}},
			},
		},
	}
	target := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: "srv"}}
	applyCiliumPolicy(t, dyn, networkpolicy.BuildEgressPolicyCiliumNetworkPolicy(policy, target))

	selected := map[string]string{networkpolicy.LabelProvider: "srv"}
	if !resolves(t, cs, ns, "probe-fqdn-resolve", selected, allowedFQDN) {
		t.Errorf("a governed pod cannot resolve %s at all: the failure below would then be about "+
			"DNS reachability rather than about the toFQDNs allow-list", allowedFQDN)
	}
	if !canReach(t, cs, ns, "probe-fqdn-allowed", selected, allowedFQDN, fqdnPort) {
		t.Errorf("the allow-listed FQDN %s is blocked with the cache in the path through the "+
			"redirect: Cilium's DNS proxy is not observing the lookup (#178)", allowedFQDN)
	}
	if canReach(t, cs, ns, "probe-fqdn-disallowed", selected, disallowedFQDN, fqdnPort) {
		t.Errorf("%s is outside the FQDN allow-list but was reachable: resolution through the "+
			"node-local cache bypasses toFQDNs enforcement", disallowedFQDN)
	}
}

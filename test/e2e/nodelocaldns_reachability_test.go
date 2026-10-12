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
// What it takes from policy: the DNS rule has to name the cache pods. After the
// redirect a lookup's destination is a node-local-dns pod, and Cilium's DNS
// proxy only inspects lookups to the endpoints the CiliumNetworkPolicy's DNS
// rule selects; with kube-dns alone the lookup goes unobserved and toFQDNs
// denies the allow-listed name, exactly as on the link-local topology. The
// operator runs on this leg with
// --dns-egress-selectors=kube-system/k8s-app=node-local-dns, and the suite's
// own builds carry the same setting.
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

// init configures the DNS-topology setting exactly as the operator does at
// startup from --dns-egress-selectors, for the same reason: every policy built
// afterwards has to carry the resolver this cluster actually uses.
func init() {
	if os.Getenv("E2E_NODELOCAL_DNS") != "1" {
		return
	}
	if err := networkpolicy.SetExtraDNSSelectors([]string{nodeLocalDNSSelector}); err != nil {
		panic(fmt.Sprintf("configuring the node-local resolver for the e2e leg: %v", err))
	}
}

// nodeLocalDNSSelector names the cache pods, matching the flag `make
// e2e-operator` gives the operator on this leg.
const nodeLocalDNSSelector = "kube-system/k8s-app=node-local-dns"

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
		t.Fatalf("lookups through kube-dns did not reach the node-local-dns cache -- the " +
			"CiliumLocalRedirectPolicy is not steering DNS, and every assertion on this leg would " +
			"pass for the wrong reason")
	}
}

// TestNodeLocalDNSCiliumFQDNNeedsTheCachePodsInTheDNSRule records why the
// operator needs --dns-egress-selectors on this topology: a CiliumNetworkPolicy
// whose DNS rule names only kube-dns lets the redirected lookup through
// unobserved, so the toFQDNs list stays empty and the allow-listed name is
// denied. TestNodeLocalDNSCiliumFQDNAllowListHolds is the same policy with the
// cache pods named, where the name connects.
func TestNodeLocalDNSCiliumFQDNNeedsTheCachePodsInTheDNSRule(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	requireCiliumLeg(t)
	cs := clientset(t)
	dyn := dynamicClient(t)
	ns := namespace(t, cs, nil)

	if !canReach(t, cs, ns, "control-allowed", map[string]string{"role": "control"}, allowedFQDN, fqdnPort) {
		t.Fatalf("%s:%d unreachable with NO policy applied", allowedFQDN, fqdnPort)
	}

	policy := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "nodelocal-kubedns-only", Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			Mode:      mcpv1alpha2.EgressPolicyModeEnforce,
			Upstreams: []mcpv1alpha2.UpstreamRule{{Match: mcpv1alpha2.UpstreamMatch{Host: allowedFQDN}}},
		},
	}
	target := metav1.LabelSelector{MatchLabels: map[string]string{networkpolicy.LabelProvider: "srv"}}
	saved := networkpolicy.ExtraDNSSelectors
	networkpolicy.ExtraDNSSelectors = nil
	kubeDNSOnly := networkpolicy.BuildEgressPolicyCiliumNetworkPolicy(policy, target)
	networkpolicy.ExtraDNSSelectors = saved
	applyCiliumPolicy(t, dyn, kubeDNSOnly)

	if canReach(t, cs, ns, "probe-fqdn-kubedns-only", map[string]string{networkpolicy.LabelProvider: "srv"},
		allowedFQDN, fqdnPort) {
		t.Errorf("the allow-listed FQDN %s connected with a DNS rule naming only kube-dns: the proxy "+
			"observes the redirected lookup after all, and --dns-egress-selectors is not needed here",
			allowedFQDN)
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

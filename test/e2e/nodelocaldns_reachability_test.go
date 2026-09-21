//go:build e2e

// The toFQDNs / NodeLocal DNSCache interaction, with real packets.
//
// The MCPEgressPolicy epic flagged this at design time and the docs have
// described it ever since with no test behind it (#145):
//
//	toFQDNs interacts with NodeLocal DNSCache; the operator's DNS-topology
//	configuration (ExtraDNSEgressPeers) covers the same ground.
//	-- guides/EGRESS_POLICY.md
//
// What it found, on the first run, is that the sentence is wrong in both of its
// halves (#178):
//
//   - The DNS egress rule is not the control point. A governed pod resolves
//     through the cache whether or not ExtraDNSEgressPeers names it, because
//     the destination is the node itself. --dns-egress-cidrs does nothing here,
//     and on the Cilium path it is not read at all.
//
//   - The allow-listed upstream is denied anyway. Cilium's toFQDNs list is
//     populated by its DNS proxy observing the lookup, and a lookup a per-node
//     cache answers is one the proxy never sees, so the list stays empty and
//     the connect to the resolved address is dropped as unmatched.
//
// Enforcement survives -- a name outside the allow-list stays blocked in every
// run -- so this fails closed. The tests below assert that behavior as it is,
// not as the guide describes it, and say at each assertion which way it should
// flip when #178 is fixed.
//
// This leg runs with NodeLocal DNSCache installed and kubelet pointed at it
// (E2E_NODELOCAL_DNS=1, `make e2e-cluster E2E_CNI=cilium E2E_NODELOCAL_DNS=1`).
// It is gated on the environment variable rather than on detecting the cache:
// a detection-based skip would turn "the cache failed to install" into a green
// run, which is the failure this file exists to stop.
package e2e

import (
	"fmt"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// init configures the DNS-topology setting exactly as the operator does at
// startup from --dns-egress-cidrs, for the same reason: every policy built
// afterwards has to carry the resolver this cluster actually uses. Doing it
// here rather than inside one test means the rest of the suite runs on this leg
// under the same configuration a real deployment would have.
func init() {
	if os.Getenv("E2E_NODELOCAL_DNS") != "1" {
		return
	}
	if err := networkpolicy.SetExtraDNSCIDRs([]string{nodeLocalDNSCIDR()}); err != nil {
		panic(fmt.Sprintf("configuring the node-local resolver for the e2e leg: %v", err))
	}
}

// nodeLocalDNSIP is the address the cache answers on, matching the Makefile's
// NODELOCAL_DNS_IP and the kubelet clusterDNS the kind config sets.
func nodeLocalDNSIP() string {
	if ip := os.Getenv("NODELOCAL_DNS_IP"); ip != "" {
		return ip
	}
	return "169.254.20.10"
}

func nodeLocalDNSCIDR() string { return nodeLocalDNSIP() + "/32" }

func requireNodeLocalDNSLeg(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_NODELOCAL_DNS") != "1" {
		t.Skip("E2E_NODELOCAL_DNS != 1: run via `make e2e E2E_CNI=cilium E2E_NODELOCAL_DNS=1`")
	}
}

// resolves reports whether a pod can resolve a name through whatever resolver
// its own resolv.conf names -- on this leg, the node-local cache. It is a
// separate question from canReach on purpose: "the allow-listed host is
// unreachable" has two very different causes, and a DNS rule that does not
// cover the resolver is the one this leg is about.
func resolves(t *testing.T, cs *kubernetes.Clientset, ns, name string, labels map[string]string, host string) bool {
	t.Helper()
	script := fmt.Sprintf("nslookup %s >/dev/null 2>&1 && echo RESOLVED || echo UNRESOLVED", host)
	return probeVerdict(t, cs, ns, name, labels, script, "RESOLVED", "UNRESOLVED")
}

// TestNodeLocalDNSCacheIsTheResolver is the precondition every other assertion
// in this file rests on. If pods are not actually resolving through the cache,
// the leg is an expensive copy of the plain cilium one and would report success
// for it.
func TestNodeLocalDNSCacheIsTheResolver(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	script := fmt.Sprintf("grep -q %s /etc/resolv.conf && echo USES_CACHE || echo USES_KUBEDNS", nodeLocalDNSIP())
	if !probeVerdict(t, cs, ns, "resolver-check", nil, script, "USES_CACHE", "USES_KUBEDNS") {
		t.Fatalf("pods do not resolve through %s -- NodeLocal DNSCache is not in the path, "+
			"and every assertion on this leg would pass for the wrong reason", nodeLocalDNSIP())
	}
}

// TestNodeLocalDNSResolutionIsNotGatedByTheDNSEgressRule records what the DNS
// egress rule does on this topology, which turned out to be nothing.
//
// The rule exists so a default-deny backstop does not take the cluster's DNS
// away, and --dns-egress-cidrs exists so it can name a resolver that is not a
// kube-dns pod. On a cluster where the resolver is a per-node cache at a
// link-local address, neither matters: the destination is the node itself, and
// a governed pod resolves through it whether or not the rule names it. Both
// configurations are asserted, because "resolution works" on its own would
// pass just as happily if the setting were load-bearing and correct -- and it
// is neither (#178).
//
// If this ever starts failing, the DNS rule has become the control point here
// after all, and the availability half below turns into a policy bug rather
// than a data-plane one.
func TestNodeLocalDNSResolutionIsNotGatedByTheDNSEgressRule(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	if !resolves(t, cs, ns, "control-resolve", map[string]string{"role": "control"}, allowedFQDN) {
		t.Fatalf("%s does not resolve with NO policy applied -- the cache is broken, and "+
			"every verdict below would be about the wrong thing", allowedFQDN)
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

	// As the operator would build it, told about the resolver.
	withPeers, _ := networkpolicy.BuildEgressPolicyBackstop(policy, target)
	applyPolicy(t, cs, withPeers)
	if !resolves(t, cs, ns, "probe-resolve-configured", selected, allowedFQDN) {
		t.Errorf("a governed pod cannot resolve %s through %s even though the DNS egress rule "+
			"names it", allowedFQDN, nodeLocalDNSIP())
	}

	// As an operator that was never told would build it. This is the case the
	// flag's help text says loses DNS, and it does not.
	saved := networkpolicy.ExtraDNSEgressPeers
	networkpolicy.ExtraDNSEgressPeers = nil
	unconfigured, _ := networkpolicy.BuildEgressPolicyBackstop(policy, target)
	networkpolicy.ExtraDNSEgressPeers = saved

	ns2 := namespace(t, cs, nil)
	unconfigured.Namespace = ns2
	applyPolicy(t, cs, unconfigured)
	if !resolves(t, cs, ns2, "probe-resolve-unconfigured", selected, allowedFQDN) {
		t.Errorf("a backstop whose DNS rule names only the kube-dns endpoints blocked resolution "+
			"of %s through %s: the DNS egress rule IS the control point on this topology after "+
			"all, and --dns-egress-cidrs is now load-bearing here", allowedFQDN, nodeLocalDNSIP())
	}
}

// TestNodeLocalDNSCiliumFQDNUpstreamsAreDenied is the finding this leg was
// built to make visible, asserted as it behaves rather than as the docs
// describe it: with a per-node DNS cache answering lookups, Cilium's DNS proxy
// never observes one, the toFQDNs allow-list is never populated, and the
// allow-listed upstream is denied along with everything else.
//
// Enforcement holds -- a name outside the allow-list stays blocked -- which is
// why this fails closed and is a bug about availability. Both halves are
// asserted, because a cluster with no egress at all would satisfy the second
// on its own.
//
// When #178 is fixed, the first assertion flips and this test fails. That is
// the intended signal: change it to require reachability then, and delete this
// paragraph.
func TestNodeLocalDNSCiliumFQDNUpstreamsAreDenied(t *testing.T) {
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
	if canReach(t, cs, ns, "probe-fqdn-allowed", selected, allowedFQDN, fqdnPort) {
		t.Errorf("the allow-listed FQDN %s is now reachable with the cache in the path -- #178 "+
			"appears to be fixed; invert this assertion", allowedFQDN)
	}
	if canReach(t, cs, ns, "probe-fqdn-disallowed", selected, disallowedFQDN, fqdnPort) {
		t.Errorf("%s is outside the FQDN allow-list but was reachable: resolution through the "+
			"node-local cache bypasses toFQDNs enforcement", disallowedFQDN)
	}
}

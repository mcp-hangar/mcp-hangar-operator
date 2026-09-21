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
// Two things can go wrong when a per-node cache is the resolver, and they fail
// in opposite directions:
//
//   - Availability. The generated DNS egress rule selects the kube-dns
//     endpoints. Pods configured to resolve through a link-local address never
//     send a packet to one, so a governed pod resolves nothing at all and every
//     upstream is unreachable -- including the ones the policy allows. This is
//     what --dns-egress-cidrs / ExtraDNSEgressPeers exists to prevent, and what
//     nothing has ever checked against a running cache.
//
//   - Enforcement. Cilium's toFQDNs allow-list is populated by its DNS proxy
//     observing the lookup. A resolution path the proxy does not see would let
//     a name resolve while the allow-list stays empty, or -- the direction that
//     matters -- let traffic out to a name that was never allow-listed.
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

// TestNodeLocalDNSVanillaBackstopKeepsResolutionWorking is the availability
// half, on the path where ExtraDNSEgressPeers is read: a Vanilla backstop is
// default-deny egress, so a DNS rule that does not name the node-local resolver
// takes the cluster's DNS away from every governed pod.
//
// The negative control is the point of the test. Asserting only that resolution
// works would pass just as happily if the setting did nothing, so the same
// policy is also built with the setting cleared, and that one must fail.
func TestNodeLocalDNSVanillaBackstopKeepsResolutionWorking(t *testing.T) {
	requireNodeLocalDNSLeg(t)
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	if !resolves(t, cs, ns, "control-resolve", map[string]string{"role": "control"}, allowedFQDN) {
		t.Fatalf("%s does not resolve with NO policy applied -- the cache is broken, and "+
			"'resolution blocked' below would be indistinguishable from 'never worked'", allowedFQDN)
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

	// With the resolver configured, as the operator would have it.
	withPeers, _ := networkpolicy.BuildEgressPolicyBackstop(policy, target)
	applyPolicy(t, cs, withPeers)
	if !resolves(t, cs, ns, "probe-resolve-configured", selected, allowedFQDN) {
		t.Errorf("a governed pod cannot resolve %s through %s even though the DNS egress rule "+
			"names it: ExtraDNSEgressPeers does not cover the node-local resolver on the wire",
			allowedFQDN, nodeLocalDNSIP())
	}

	// The control: the same policy as it would be built by an operator that was
	// never told about the cache. This must take DNS away.
	saved := networkpolicy.ExtraDNSEgressPeers
	networkpolicy.ExtraDNSEgressPeers = nil
	unconfigured, _ := networkpolicy.BuildEgressPolicyBackstop(policy, target)
	networkpolicy.ExtraDNSEgressPeers = saved

	ns2 := namespace(t, cs, nil)
	unconfigured.Namespace = ns2
	applyPolicy(t, cs, unconfigured)
	if resolves(t, cs, ns2, "probe-resolve-unconfigured", selected, allowedFQDN) {
		t.Errorf("a governed pod still resolved %s under a backstop whose DNS rule names only the "+
			"kube-dns endpoints: this leg is not exercising the node-local resolver, so the "+
			"assertion above proves nothing", allowedFQDN)
	}
}

// TestNodeLocalDNSCiliumFQDNAllowListStillHolds is the enforcement half: a
// resolution path that Cilium's DNS proxy does not observe cannot be allowed to
// become a way around the toFQDNs allow-list.
func TestNodeLocalDNSCiliumFQDNAllowListStillHolds(t *testing.T) {
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
		t.Errorf("a governed pod cannot resolve %s through %s: the Cilium backstop's DNS egress "+
			"rule does not reach the node-local resolver", allowedFQDN, nodeLocalDNSIP())
	}
	if !canReach(t, cs, ns, "probe-fqdn-allowed", selected, allowedFQDN, fqdnPort) {
		t.Errorf("the allow-listed FQDN %s is blocked with the cache in the path: either the DNS "+
			"rule does not cover it, or Cilium's proxy never saw the lookup and the toFQDNs "+
			"allow-list stayed empty", allowedFQDN)
	}
	if canReach(t, cs, ns, "probe-fqdn-disallowed", selected, disallowedFQDN, fqdnPort) {
		t.Errorf("%s is outside the FQDN allow-list but was reachable: resolution through the "+
			"node-local cache bypasses toFQDNs enforcement", disallowedFQDN)
	}
}

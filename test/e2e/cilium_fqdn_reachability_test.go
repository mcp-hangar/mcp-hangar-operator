//go:build e2e

// Cilium-flavor reachability: the toFQDNs rules emitted by
// BuildEgressPolicyCiliumNetworkPolicy, checked against real packets.
//
// The tests in networkpolicy_reachability_test.go cover the Vanilla path,
// where a hostname upstream is failed closed. The Cilium flavor is the only
// path where a hostname upstream is actually ENFORCED: the DNS egress rule
// carries an L7 DNS rule so Cilium's proxy learns the resolved IPs, and only
// traffic to allow-listed names is admitted. DNS-based policy has failure
// modes CIDR policy does not (the proxy must observe the lookup before the
// connect), so asserting on the built object is not enough here either.
//
// These tests run only on the cilium leg of the e2e matrix (E2E_CNI=cilium,
// cluster built by `make e2e-cluster E2E_CNI=cilium`). They are gated on the
// environment variable rather than on detecting the Cilium CRD: if the cilium
// leg somehow ran without Cilium installed, a detection-based skip would turn
// that into a green run for the wrong reason, whereas this way the CNP apply
// fails loudly.
//
// The destinations are real external hosts. Cilium translates toFQDNs into
// IP-based rules, and IP-based rules do not select cluster-internal
// endpoints, so an in-cluster Service DNS name cannot stand in.
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

const (
	// Stable anycast destination with 443 open; the allow-listed upstream.
	allowedFQDN = "one.one.one.one"
	// Stable and resolvable, but NOT on the allow-list; must be blocked.
	disallowedFQDN = "example.com"
	fqdnPort       = 443
	// Longer than policySettle: Cilium must program the per-endpoint policy
	// AND stand up the DNS proxy for the selected endpoints.
	ciliumPolicySettle = 10 * time.Second
)

var ciliumNetworkPolicyGVR = schema.GroupVersionResource{
	Group:    networkpolicy.CiliumGroup,
	Version:  networkpolicy.CiliumVersion,
	Resource: "ciliumnetworkpolicies",
}

func requireCiliumLeg(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_CNI") != "cilium" {
		t.Skip("E2E_CNI != cilium: only Cilium enforces toFQDNs; run via `make e2e E2E_CNI=cilium`")
	}
}

func dynamicClient(t *testing.T) dynamic.Interface {
	t.Helper()
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		path = os.Getenv("HOME") + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	return dyn
}

func applyCiliumPolicy(t *testing.T, dyn dynamic.Interface, cnp *unstructured.Unstructured) {
	t.Helper()
	if cnp == nil {
		t.Fatal("builder returned nil CiliumNetworkPolicy -- the test would prove nothing")
	}
	if _, err := dyn.Resource(ciliumNetworkPolicyGVR).Namespace(cnp.GetNamespace()).
		Create(context.Background(), cnp, metav1.CreateOptions{}); err != nil {
		// Deliberately fatal rather than a skip: on the cilium leg, "the CNP
		// cannot be applied" (e.g. CRD missing because Cilium is not actually
		// installed) must be red, not silently green.
		t.Fatalf("apply CiliumNetworkPolicy %s: %v", cnp.GetName(), err)
	}
	time.Sleep(ciliumPolicySettle)
}

// TestCiliumFQDNAllowedConnectsAndDisallowedIsBlocked is the headline claim of
// the Cilium flavor: an MCPEgressPolicy upstream given as a hostname is
// enforced on the wire -- the allow-listed name connects, everything else is
// dropped -- via the toFQDNs rules the builder emits.
func TestCiliumFQDNAllowedConnectsAndDisallowedIsBlocked(t *testing.T) {
	requireCiliumLeg(t)
	cs := clientset(t)
	dyn := dynamicClient(t)
	ns := namespace(t, cs, nil)

	// Controls, before any policy exists: both destinations must be reachable
	// from an unconstrained pod, or the deny assertion below would pass for
	// the wrong reason (no internet egress, DNS broken, pods not starting).
	if !canReach(t, cs, ns, "control-allowed", map[string]string{"role": "control"},
		allowedFQDN, fqdnPort) {
		t.Fatalf("%s:%d unreachable with NO policy applied -- the cluster has no external egress, "+
			"and every deny assertion here would pass for the wrong reason", allowedFQDN, fqdnPort)
	}
	if !canReach(t, cs, ns, "control-disallowed", map[string]string{"role": "control"},
		disallowedFQDN, fqdnPort) {
		t.Fatalf("%s:%d unreachable with NO policy applied -- 'blocked' below would be "+
			"indistinguishable from 'was never reachable'", disallowedFQDN, fqdnPort)
	}

	// The SAME builder the reconciler calls, fed an MCPEgressPolicy whose
	// upstream is a hostname -- the case the Vanilla flavor fails closed.
	policy := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "fqdn-allow", Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			Mode: mcpv1alpha2.EgressPolicyModeEnforce,
			Upstreams: []mcpv1alpha2.UpstreamRule{
				{Match: mcpv1alpha2.UpstreamMatch{Host: allowedFQDN}},
			},
		},
	}
	target := metav1.LabelSelector{
		MatchLabels: map[string]string{networkpolicy.LabelProvider: "srv"},
	}
	applyCiliumPolicy(t, dyn, networkpolicy.BuildEgressPolicyCiliumNetworkPolicy(policy, target))

	selected := map[string]string{networkpolicy.LabelProvider: "srv"}
	if !canReach(t, cs, ns, "probe-fqdn-allowed", selected, allowedFQDN, fqdnPort) {
		t.Errorf("the allow-listed FQDN %s is blocked; toFQDNs denies what the policy permits", allowedFQDN)
	}
	if canReach(t, cs, ns, "probe-fqdn-disallowed", selected, disallowedFQDN, fqdnPort) {
		t.Errorf("%s is outside the FQDN allow-list but was reachable; toFQDNs egress is not enforced", disallowedFQDN)
	}
}

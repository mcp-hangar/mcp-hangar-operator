// Package networkpolicy contains utilities for generating Kubernetes NetworkPolicy
// resources from MCPServer capability declarations.
package networkpolicy

import (
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcpv1alpha1 "github.com/mcp-hangar/operator/api/v1alpha1"
	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

const (
	// LabelManagedBy identifies resources managed by the operator.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelProvider identifies the owning MCPServer.
	LabelProvider = "mcp-hangar.io/provider"
	// LabelComponent classifies the resource type within the operator.
	LabelComponent = "mcp-hangar.io/component"

	// AnnotationHostWarnings lists host/FQDN-only egress rules (no CIDR) that
	// are NOT enforced by this NetworkPolicy. Such rules are failed closed (no
	// egress rule is emitted for them); enforcement is deferred to the Tetragon
	// backend (ADR-006 v1.5).
	AnnotationHostWarnings = "mcp-hangar.io/host-warnings"

	// DefaultManagerName is the value for managed-by labels.
	DefaultManagerName = "mcp-hangar-operator"

	// componentNetworkPolicy is the component label value.
	componentNetworkPolicy = "network-policy"

	// EnforceEgressLabel opts a namespace into default-deny egress (#51): the
	// operator creates a namespace-wide deny-all-egress (DNS-only) policy so pods
	// not covered by a per-server allow policy get no egress to upstreams.
	EnforceEgressLabel = "mcp-hangar.io/enforce-egress"

	// DefaultDenyEgressName is the name of the namespace default-deny policy.
	DefaultDenyEgressName = "mcp-default-deny-egress"

	// LabelEgressPolicy identifies the owning MCPEgressPolicy on a generated
	// backstop NetworkPolicy.
	LabelEgressPolicy = "mcp-hangar.io/egress-policy"
)

// NetworkPolicyName returns the canonical name for a provider's egress NetworkPolicy.
func NetworkPolicyName(providerName string) string {
	return "mcp-provider-" + providerName + "-egress"
}

// BuildNetworkPolicy translates an MCPServer's capability declarations into a
// Kubernetes NetworkPolicy resource. Returns nil if the provider declares no
// network capabilities.
//
// This is a pure function with no side effects -- it reads from the provider
// struct and returns a new NetworkPolicy. The reconciler is responsible for
// creating/updating the resource in the cluster.
func BuildNetworkPolicy(provider *mcpv1alpha1.MCPServer) *networkingv1.NetworkPolicy {
	if provider.Spec.Capabilities == nil || provider.Spec.Capabilities.Network == nil {
		return nil
	}

	caps := provider.Spec.Capabilities.Network

	annotations := map[string]string{}
	warnings := hostWarnings(caps)
	if warnings != "" {
		annotations[AnnotationHostWarnings] = warnings
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NetworkPolicyName(provider.Name),
			Namespace:   provider.Namespace,
			Labels:      buildLabels(provider),
			Annotations: annotations,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					LabelProvider: provider.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: buildEgressRules(caps),
		},
	}
}

// BuildNamespaceDefaultDenyEgress returns a namespace-wide NetworkPolicy that
// denies all egress except DNS (scoped to kube-dns). It selects every pod in
// the namespace; per-server allow policies are additive, so a registered server
// keeps its declared egress while any pod not covered by one -- an unregistered
// or shadow workload -- is limited to DNS and reaches no upstream. Applied only
// to namespaces opted in via EnforceEgressLabel (#51).
func BuildNamespaceDefaultDenyEgress(namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultDenyEgressName,
			Namespace: namespace,
			Labels: map[string]string{
				LabelManagedBy: DefaultManagerName,
				LabelComponent: componentNetworkPolicy,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Empty selector = all pods in the namespace.
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			// Only DNS is allowed; everything else is denied unless another
			// (per-server) policy additively opens it.
			Egress: []networkingv1.NetworkPolicyEgressRule{dnsEgressRule()},
		},
	}
}

// EgressPolicyBackstopName returns the canonical name for the L3/L4 backstop
// NetworkPolicy generated for an MCPEgressPolicy.
func EgressPolicyBackstopName(policyName string) string {
	return "mcp-egresspolicy-" + policyName
}

// BuildEgressPolicyBackstop builds the Vanilla L3/L4 network backstop for an
// MCPEgressPolicy: a default-deny egress NetworkPolicy scoped to the target's
// pods that allows DNS plus any upstream whose host is a literal IP or CIDR.
//
// Vanilla NetworkPolicy cannot match on DNS/FQDN, so upstreams given as a
// hostname are FAILED CLOSED (no rule emitted -- they are denied, not opened to
// any destination) and returned in unenforceableHosts so the caller can surface
// the gap. Enforcing FQDN allow-lists is the Cilium flavor's job (toFQDNs),
// which lands in a follow-up.
func BuildEgressPolicyBackstop(policy *mcpv1alpha2.MCPEgressPolicy, target metav1.LabelSelector) (np *networkingv1.NetworkPolicy, unenforceableHosts []string) {
	egress := []networkingv1.NetworkPolicyEgressRule{dnsEgressRule()}

	for _, u := range policy.Spec.Upstreams {
		if cidr, ok := asCIDR(u.Match.Host); ok {
			egress = append(egress, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{
					{IPBlock: &networkingv1.IPBlock{CIDR: cidr}},
				},
			})
		} else {
			unenforceableHosts = append(unenforceableHosts, u.Match.Host)
		}
	}

	np = &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      EgressPolicyBackstopName(policy.Name),
			Namespace: policy.Namespace,
			Labels: map[string]string{
				LabelManagedBy:    DefaultManagerName,
				LabelComponent:    componentNetworkPolicy,
				LabelEgressPolicy: policy.Name,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: target,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      egress,
		},
	}
	return np, unenforceableHosts
}

// Cilium GroupVersion/Kind for the CiliumNetworkPolicy backstop. We do not
// vendor Cilium's Go types; the backstop is built as an unstructured object.
const (
	CiliumGroup             = "cilium.io"
	CiliumVersion           = "v2"
	CiliumNetworkPolicyKind = "CiliumNetworkPolicy"
)

// BuildEgressPolicyCiliumNetworkPolicy builds the Cilium L3/L4 backstop for an
// MCPEgressPolicy as an unstructured CiliumNetworkPolicy. Unlike the Vanilla
// NetworkPolicy, this enforces FQDN upstreams via toFQDNs: the DNS egress rule
// carries an L7 DNS rule (matchPattern "*") so Cilium's DNS proxy learns the
// resolved IPs and admits only traffic to the allow-listed names. CIDR
// upstreams become toCIDR rules.
//
// The returned object carries its GVK and owner-friendly labels; the caller
// sets the controller reference.
func BuildEgressPolicyCiliumNetworkPolicy(policy *mcpv1alpha2.MCPEgressPolicy, targetProvider string) *unstructured.Unstructured {
	// DNS egress to kube-dns with the L7 DNS proxy enabled (required for toFQDNs).
	egress := []interface{}{
		map[string]interface{}{
			"toEndpoints": []interface{}{
				map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"k8s:io.kubernetes.pod.namespace": "kube-system",
						"k8s-app":                         "kube-dns",
					},
				},
			},
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{"port": "53", "protocol": "ANY"},
					},
					"rules": map[string]interface{}{
						"dns": []interface{}{
							map[string]interface{}{"matchPattern": "*"},
						},
					},
				},
			},
		},
	}

	var fqdns, cidrs []interface{}
	for _, u := range policy.Spec.Upstreams {
		if cidr, ok := asCIDR(u.Match.Host); ok {
			cidrs = append(cidrs, cidr)
		} else {
			fqdns = append(fqdns, map[string]interface{}{"matchName": u.Match.Host})
		}
	}
	if len(fqdns) > 0 {
		egress = append(egress, map[string]interface{}{"toFQDNs": fqdns})
	}
	if len(cidrs) > 0 {
		egress = append(egress, map[string]interface{}{"toCIDR": cidrs})
	}

	cnp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":      EgressPolicyBackstopName(policy.Name),
				"namespace": policy.Namespace,
				"labels": map[string]interface{}{
					LabelManagedBy:    DefaultManagerName,
					LabelComponent:    componentNetworkPolicy,
					LabelEgressPolicy: policy.Name,
				},
			},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						LabelProvider: targetProvider,
					},
				},
				"egress": egress,
			},
		},
	}
	cnp.SetGroupVersionKind(schema.GroupVersionKind{Group: CiliumGroup, Version: CiliumVersion, Kind: CiliumNetworkPolicyKind})
	return cnp
}

// asCIDR normalizes a host that is a literal IP or CIDR into a CIDR string. A
// hostname (FQDN) returns ok=false.
func asCIDR(host string) (cidr string, ok bool) {
	if _, _, err := net.ParseCIDR(host); err == nil {
		return host, true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return host + "/32", true
		}
		return host + "/128", true
	}
	return "", false
}

// buildLabels returns standard labels for the NetworkPolicy resource.
func buildLabels(provider *mcpv1alpha1.MCPServer) map[string]string {
	return map[string]string{
		LabelManagedBy: DefaultManagerName,
		LabelProvider:  provider.Name,
		LabelComponent: componentNetworkPolicy,
	}
}

// buildEgressRules constructs the full list of egress rules from the network
// capabilities spec. Order: DNS (if allowed), loopback (if allowed), then
// declared egress rules in declaration order.
func buildEgressRules(caps *mcpv1alpha1.NetworkCapabilitiesSpec) []networkingv1.NetworkPolicyEgressRule {
	var rules []networkingv1.NetworkPolicyEgressRule

	// DNS: default-allow unless explicitly disabled
	if caps.DNSAllowed == nil || *caps.DNSAllowed {
		rules = append(rules, dnsEgressRule())
	}

	// Loopback: only if explicitly enabled
	if caps.LoopbackAllowed != nil && *caps.LoopbackAllowed {
		rules = append(rules, loopbackEgressRule())
	}

	// Declared egress rules. Host/FQDN-only rules are skipped (fail closed) --
	// see translateEgressRule.
	for _, rule := range caps.Egress {
		if egressRule, ok := translateEgressRule(rule); ok {
			rules = append(rules, egressRule)
		}
	}

	return rules
}

// translateEgressRule converts a single EgressRuleSpec into a NetworkPolicy
// egress rule. The bool return reports whether an egress rule was produced;
// host/FQDN-only rules are skipped and report false.
//
// Only CIDR rules produce an enforceable IPBlock peer. A host/FQDN-only rule
// (Host set, no CIDR) is FAILED CLOSED: it produces no egress rule at all.
// Emitting a port-only rule (Ports set, no To) would be interpreted by
// Kubernetes as "allow this port to ANY destination", silently downgrading a
// hostname allowlist into an all-destinations egress opening (SSRF /
// data-exfiltration vector). Vanilla NetworkPolicy cannot match on DNS/FQDN, so
// we refuse to open the port rather than open it too widely. Such rules are
// still surfaced via the host-warnings annotation and a validating-webhook
// warning. FQDN egress enforcement is deferred to the Tetragon backend
// (ADR-006 v1.5).
//
// Port 0 means "any port" and omits the Ports field entirely.
func translateEgressRule(rule mcpv1alpha1.EgressRuleSpec) (networkingv1.NetworkPolicyEgressRule, bool) {
	// Fail closed: host/FQDN-only rules (no CIDR) cannot be enforced by
	// NetworkPolicy. Do not emit a permissive port-only rule.
	if rule.CIDR == "" {
		return networkingv1.NetworkPolicyEgressRule{}, false
	}

	egressRule := networkingv1.NetworkPolicyEgressRule{
		// Peer: IPBlock from CIDR
		To: []networkingv1.NetworkPolicyPeer{
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: rule.CIDR,
				},
			},
		},
	}

	// Ports: omit for port=0 (any port)
	if rule.Port > 0 {
		egressRule.Ports = []networkingv1.NetworkPolicyPort{
			{
				Port:     portPtr(rule.Port),
				Protocol: mapProtocol(rule.Protocol),
			},
		}
	}

	return egressRule, true
}

// mapProtocol converts an application-level protocol hint to a Kubernetes network
// protocol. "https", "http", "grpc", and "tcp" all map to TCP. "any" or empty
// returns nil (meaning any protocol).
func mapProtocol(protocol string) *corev1.Protocol {
	switch strings.ToLower(protocol) {
	case "https", "http", "grpc", "tcp":
		p := corev1.ProtocolTCP
		return &p
	default:
		// "any" or "" -- allow any protocol
		return nil
	}
}

// ExtraDNSEgressPeers are appended to the DNS egress rule's destination peers,
// for clusters whose resolver is not the in-cluster kube-dns pods -- NodeLocal
// DNSCache (pods query a node-local link address, typically 169.254.20.10) or a
// custom resolver. The operator sets this once at startup from configuration;
// the default (empty) yields the kube-dns-only rule. Without it, scoping DNS to
// kube-dns would break resolution on NodeLocal-DNSCache clusters -- turning the
// #56 security fix into an availability incident. See also the toFQDNs gotcha in
// the MCPEgressPolicy epic (#53); both want the same DNS-topology config.
var ExtraDNSEgressPeers []networkingv1.NetworkPolicyPeer

// SetExtraDNSCIDRs configures ExtraDNSEgressPeers from CIDR strings (e.g. a
// NodeLocal DNSCache address like "169.254.20.10/32"). Called once at operator
// startup; returns an error on the first unparseable CIDR.
func SetExtraDNSCIDRs(cidrs []string) error {
	peers := make([]networkingv1.NetworkPolicyPeer, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("invalid DNS egress CIDR %q: %w", c, err)
		}
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: c},
		})
	}
	ExtraDNSEgressPeers = peers
	return nil
}

// kubeDNSPeer selects the in-cluster DNS pods (both CoreDNS and kube-dns carry
// k8s-app=kube-dns) in kube-system.
func kubeDNSPeer() networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
		},
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"k8s-app": "kube-dns"},
		},
	}
}

// dnsEgressRule returns an egress rule allowing DNS queries (UDP + TCP port 53)
// to the cluster DNS pods (plus any ExtraDNSEgressPeers).
//
// The destination is SCOPED, not left open: a rule with ports but no peer means
// "port 53 to ANY destination", which turns every egress-scoped pod into an open
// :53 channel a DNS-tunnel C2 can exfiltrate through -- the egress allow-list
// does not constrain it. See #56.
func dnsEgressRule() networkingv1.NetworkPolicyEgressRule {
	to := append([]networkingv1.NetworkPolicyPeer{kubeDNSPeer()}, ExtraDNSEgressPeers...)
	return networkingv1.NetworkPolicyEgressRule{
		To: to,
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Port:     portPtr(53),
				Protocol: protocolPtr(corev1.ProtocolUDP),
			},
			{
				Port:     portPtr(53),
				Protocol: protocolPtr(corev1.ProtocolTCP),
			},
		},
	}
}

// loopbackEgressRule returns an egress rule allowing traffic to 127.0.0.0/8.
func loopbackEgressRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: "127.0.0.0/8",
				},
			},
		},
	}
}

// hostWarnings returns a comma-separated list of host-only egress rules (those
// without CIDR) that cannot be enforced at the network level.
func hostWarnings(caps *mcpv1alpha1.NetworkCapabilitiesSpec) string {
	var warnings []string
	for _, rule := range caps.Egress {
		if rule.CIDR == "" && rule.Host != "" {
			warnings = append(warnings, rule.Host)
		}
	}
	return strings.Join(warnings, ",")
}

// portPtr returns a pointer to an intstr.IntOrString from an int32 port number.
func portPtr(port int32) *intstr.IntOrString {
	p := intstr.FromInt32(port)
	return &p
}

// protocolPtr returns a pointer to a corev1.Protocol.
func protocolPtr(p corev1.Protocol) *corev1.Protocol {
	return &p
}

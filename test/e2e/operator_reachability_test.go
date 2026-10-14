//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

// The tests in networkpolicy_reachability_test.go apply the builder's output
// themselves, so they prove the YAML, not the operator. These create the CRs
// and let the operator `make e2e-operator` runs (built from this checkout)
// write the policies; the probes then test what the operator wrote (#208).

// operatorSettle bounds how long the operator gets to write a policy, after
// which the CNI still needs policySettle to program it.
const operatorSettle = 90 * time.Second

func crClient(t *testing.T) crclient.Client {
	t.Helper()
	path := kubeconfigPath()
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := mcpv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("mcp scheme: %v", err)
	}
	cl, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return cl
}

// coldServer is a container-mode MCPServer with replicas 0: the operator
// writes its NetworkPolicy but runs no pod, so the test's probe pods, which
// carry the provider label, are the only pods the policy selects.
func coldServer(name, ns string, egress []mcpv1alpha2.EgressRuleSpec) *mcpv1alpha2.MCPServer {
	zero := int32(0)
	s := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeContainer,
			Image:    "busybox:1.36",
			Replicas: &zero,
		},
	}
	if egress != nil {
		s.Spec.Capabilities = &mcpv1alpha2.MCPServerCapabilities{
			Network: &mcpv1alpha2.NetworkCapabilitiesSpec{Egress: egress},
		}
	}
	return s
}

func createCR(t *testing.T, cl crclient.Client, obj crclient.Object) {
	t.Helper()
	if err := cl.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
}

// waitForOperatorPolicy waits until the operator has written the named
// NetworkPolicy, checks that the CR named owner controls it, and gives the CNI
// time to program it.
func waitForOperatorPolicy(t *testing.T, cl crclient.Client, ns, name, owner string) {
	t.Helper()
	np := &networkingv1.NetworkPolicy{}
	deadline := time.Now().Add(operatorSettle)
	for {
		err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, np)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the operator never wrote NetworkPolicy %s/%s: %v", ns, name, err)
		}
		time.Sleep(2 * time.Second)
	}
	ref := metav1.GetControllerOf(np)
	if ref == nil || ref.Name != owner {
		t.Fatalf("NetworkPolicy %s/%s is not controlled by %q (controller: %+v)", ns, name, owner, ref)
	}
	time.Sleep(policySettle)
}

// TestOperator_MCPServerEgressIsEnforced: an MCPServer's declared CIDR egress
// opens exactly that destination once the operator has acted on it.
func TestOperator_MCPServerEgressIsEnforced(t *testing.T) {
	cs := clientset(t)
	cl := crClient(t)
	ns := namespace(t, cs, nil)

	allowed := runningPod(t, cs, ns, "allowed-target",
		map[string]string{"role": "allowed"}, []string{"nc -lk -p 8080 -e /bin/true"})
	denied := runningPod(t, cs, ns, "denied-target",
		map[string]string{"role": "denied"}, []string{"nc -lk -p 8080 -e /bin/true"})

	createCR(t, cl, coldServer("srv", ns, []mcpv1alpha2.EgressRuleSpec{
		{Host: "allowed", CIDR: allowed.Status.PodIP + "/32", Port: 8080},
	}))
	waitForOperatorPolicy(t, cl, ns, networkpolicy.NetworkPolicyName("srv"), "srv")

	clientLabels := map[string]string{networkpolicy.LabelProvider: "srv"}
	if !canReach(t, cs, ns, "probe-allowed", clientLabels, allowed.Status.PodIP, 8080) {
		t.Error("the operator's policy blocked the CIDR the MCPServer declared")
	}
	if canReach(t, cs, ns, "probe-denied", clientLabels, denied.Status.PodIP, 8080) {
		t.Error("traffic outside the MCPServer's declared egress got through the operator's policy")
	}
}

// TestOperator_EgressPolicyBackstopIsEnforced: an MCPEgressPolicy in Enforce
// mode gets an L3/L4 backstop from the operator (a NetworkPolicy, or a
// CiliumNetworkPolicy where Cilium runs) that admits its CIDR upstream and
// nothing else. The target declares no egress of its own, so the backstop is
// the only policy selecting the probe pods.
func TestOperator_EgressPolicyBackstopIsEnforced(t *testing.T) {
	cs := clientset(t)
	cl := crClient(t)
	ns := namespace(t, cs, nil)

	allowed := runningPod(t, cs, ns, "allowed-target",
		map[string]string{"role": "allowed"}, []string{"nc -lk -p 8080 -e /bin/true"})
	denied := runningPod(t, cs, ns, "denied-target",
		map[string]string{"role": "denied"}, []string{"nc -lk -p 8080 -e /bin/true"})

	createCR(t, cl, coldServer("target", ns, nil))
	createCR(t, cl, &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: ns},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			Mode:      mcpv1alpha2.EgressPolicyModeEnforce,
			TargetRef: mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: "target"},
			Upstreams: []mcpv1alpha2.UpstreamRule{{
				Name:  "allowed",
				Match: mcpv1alpha2.UpstreamMatch{Host: allowed.Status.PodIP + "/32"},
			}},
		},
	})

	// Which flavor the operator picks depends on the leg; either way it must
	// report the backstop applied before the probes mean anything.
	var last *mcpv1alpha2.MCPEgressPolicy
	deadline := time.Now().Add(operatorSettle)
	for time.Now().Before(deadline) {
		last = &mcpv1alpha2.MCPEgressPolicy{}
		if err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "pol"}, last); err == nil &&
			conditionTrue(last.Status.Conditions, "BackstopApplied") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if last == nil || !conditionTrue(last.Status.Conditions, "BackstopApplied") {
		t.Fatalf("the operator never applied the backstop; status: %+v", last.Status)
	}
	t.Logf("backstop enforcement: %s", last.Status.BackstopEnforcement)
	time.Sleep(policySettle)

	clientLabels := map[string]string{networkpolicy.LabelProvider: "target"}
	if !canReach(t, cs, ns, "probe-allowed", clientLabels, allowed.Status.PodIP, 8080) {
		t.Error("the operator's backstop blocked the policy's CIDR upstream")
	}
	if canReach(t, cs, ns, "probe-denied", clientLabels, denied.Status.PodIP, 8080) {
		t.Error("traffic to a destination the policy does not list got through the operator's backstop")
	}
}

func conditionTrue(conds []metav1.Condition, condType string) bool {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// The operator is configured with a gateway namespace but global ingress is
// off. This CR opts in, then out; unrelated labels must not authorize a peer.
func TestOperator_ProviderIngress_GatewayOnly(t *testing.T) {
	cs, cl := clientset(t), crClient(t)
	ns := namespace(t, cs, nil)
	target := runningPod(t, cs, ns, "provider", map[string]string{networkpolicy.LabelProvider: "fenced"}, []string{"nc -lk -p 8080 -e /bin/true"})
	server := coldServer("fenced", ns, nil)
	server.Annotations = map[string]string{networkpolicy.AnnotationProviderIngress: "true"}
	createCR(t, cl, server)
	waitForOperatorPolicy(t, cl, ns, networkpolicy.ProviderIngressPolicyName(server.Name), server.Name)
	gateway := map[string]string{"app.kubernetes.io/name": "mcp-hangar"}
	if !canReach(t, cs, "mcp-hangar", "ingress-gateway", gateway, target.Status.PodIP, 8080) {
		t.Error("gateway could not reach fenced provider")
	}
	if canReach(t, cs, ns, "ingress-client", nil, target.Status.PodIP, 8080) {
		t.Error("non-gateway client bypassed ingress fence")
	}
	if canReach(t, cs, ns, "ingress-label-spoof", gateway, target.Status.PodIP, 8080) {
		t.Error("gateway label in wrong namespace bypassed fence")
	}
	if canReach(t, cs, "mcp-hangar", "ingress-wrong-label", nil, target.Status.PodIP, 8080) {
		t.Error("unmatched pod in gateway namespace bypassed fence")
	}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: ns}, server); err != nil {
		t.Fatal(err)
	}
	var applied bool
	for _, c := range server.Status.Conditions {
		if c.Type == "ProviderIngressApplied" && c.Status == metav1.ConditionTrue {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("ingress status did not report observed enforcer: %+v", server.Status.Conditions)
	}
	server.Annotations[networkpolicy.AnnotationProviderIngress] = "false"
	if err := cl.Update(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(operatorSettle)
	for {
		err := cl.Get(context.Background(), types.NamespacedName{Name: networkpolicy.ProviderIngressPolicyName(server.Name), Namespace: ns}, &networkingv1.NetworkPolicy{})
		if apierrors.IsNotFound(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("opt-out did not remove ingress policy: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(policySettle)
	if !canReach(t, cs, ns, "ingress-opt-out", nil, target.Status.PodIP, 8080) {
		t.Error("opt-out left provider ingress blocked")
	}
}

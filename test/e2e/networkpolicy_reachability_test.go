//go:build e2e

// Package e2e verifies that the NetworkPolicies this operator builds actually
// block traffic, on a cluster with a CNI that enforces them.
//
// Why this exists separately from pkg/networkpolicy/*_test.go: those tests are
// good and they assert on API objects. 104 assertions, none of which can tell
// you whether a packet is dropped. envtest cannot help either -- it is an
// apiserver and etcd with no controller-manager and no CNI, so a NetworkPolicy
// there is a stored document, nothing more.
//
// For a project whose positioning is an enforcement plane, "we emitted the
// right YAML" and "egress is blocked" are different claims, and only the second
// one is the product.
//
// The tests below drive the SAME builder functions the reconciler calls, so a
// change to the builder is exercised here rather than a hand-written copy of
// what the builder is believed to emit.
//
// Requires: a cluster whose CNI enforces NetworkPolicy (kind's default kindnet
// does NOT -- see `make e2e-cluster`, which builds kind + Calico).
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/networkpolicy"
)

const (
	probeTimeout = 8 * time.Second
	// Long enough for Calico to program the policy after the API write. A
	// too-short wait would make a working policy look broken and vice versa.
	policySettle = 5 * time.Second
)

func clientset(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		path = os.Getenv("HOME") + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return cs
}

// namespace creates a throwaway namespace and returns its name.
func namespace(t *testing.T, cs *kubernetes.Clientset, labels map[string]string) string {
	t.Helper()
	name := fmt.Sprintf("np-e2e-%d", time.Now().UnixNano()%1_000_000)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	return name
}

// runningPod starts a pod and waits for it to be Running with an IP.
func runningPod(t *testing.T, cs *kubernetes.Clientset, ns, name string, labels map[string]string, args []string) *corev1.Pod {
	t.Helper()
	spec := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "main",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c"},
				Args:    args,
			}},
		},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(context.Background(), spec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := cs.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil && p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
			return p
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("pod %s never became Running with an IP", name)
	return nil
}

// canReach reports whether `from` can open a TCP connection to addr:port.
//
// Implemented by running the probe as the pod's own command and reading its
// exit code, rather than via exec: exec needs SPDY streaming that is awkward to
// set up from a plain clientset, and a one-shot pod gives the same answer with
// less machinery. A connection that is *denied* by a NetworkPolicy hangs rather
// than being refused, which is why the probe carries its own timeout -- the
// timeout expiring IS the deny signal.
func canReach(t *testing.T, cs *kubernetes.Clientset, ns, name string, labels map[string]string, addr string, port int) bool {
	t.Helper()
	probe := fmt.Sprintf("nc -w %d -z %s %d && echo REACHED || echo BLOCKED",
		int(probeTimeout.Seconds()), addr, port)
	runningPodNoWait(t, cs, ns, name, labels, []string{probe})

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := cs.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil && (p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed) {
			logs, lerr := cs.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(context.Background())
			if lerr != nil {
				t.Fatalf("logs for %s: %v", name, lerr)
			}
			out := string(logs)
			switch {
			case contains(out, "REACHED"):
				return true
			case contains(out, "BLOCKED"):
				return false
			default:
				t.Fatalf("probe %s produced no verdict, output: %q", name, out)
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("probe %s did not finish", name)
	return false
}

func runningPodNoWait(t *testing.T, cs *kubernetes.Clientset, ns, name string, labels map[string]string, args []string) {
	t.Helper()
	spec := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c"},
				Args:    args,
			}},
		},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(context.Background(), spec, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create probe %s: %v", name, err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func applyPolicy(t *testing.T, cs *kubernetes.Clientset, np *networkingv1.NetworkPolicy) {
	t.Helper()
	if np == nil {
		t.Fatal("builder returned nil policy -- the test would prove nothing")
	}
	if _, err := cs.NetworkingV1().NetworkPolicies(np.Namespace).Create(context.Background(), np, metav1.CreateOptions{}); err != nil {
		t.Fatalf("apply policy %s: %v", np.Name, err)
	}
	time.Sleep(policySettle)
}

func serverWithEgress(name, ns string, egress []mcpv1alpha2.EgressRuleSpec) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1alpha2.MCPServerSpec{
			Capabilities: &mcpv1alpha2.MCPServerCapabilities{
				Network: &mcpv1alpha2.NetworkCapabilitiesSpec{Egress: egress},
			},
		},
	}
}

// TestCIDRRuleAllowsAndOtherDestinationsAreBlocked is the baseline claim: a CIDR
// egress rule opens exactly that CIDR and nothing else.
func TestCIDRRuleAllowsAndOtherDestinationsAreBlocked(t *testing.T) {
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	allowed := runningPod(t, cs, ns, "allowed-target",
		map[string]string{"role": "allowed"}, []string{"nc -lk -p 8080 -e /bin/true"})
	denied := runningPod(t, cs, ns, "denied-target",
		map[string]string{"role": "denied"}, []string{"nc -lk -p 8080 -e /bin/true"})

	server := serverWithEgress("srv", ns, []mcpv1alpha2.EgressRuleSpec{
		{Host: "allowed", CIDR: allowed.Status.PodIP + "/32", Port: 8080},
	})
	applyPolicy(t, cs, networkpolicy.BuildNetworkPolicy(server))

	clientLabels := map[string]string{networkpolicy.LabelProvider: "srv"}
	if !canReach(t, cs, ns, "probe-allowed", clientLabels, allowed.Status.PodIP, 8080) {
		t.Error("traffic to the allowed CIDR was blocked; the policy denies what it should permit")
	}
	if canReach(t, cs, ns, "probe-denied", clientLabels, denied.Status.PodIP, 8080) {
		t.Error("traffic to a destination outside the allowlist got through; egress is not enforced")
	}
}

// TestHostOnlyRuleFailsClosed is the security-critical one.
//
// translateEgressRule refuses to emit a rule for a host/FQDN-only entry, on the
// stated grounds that a port-only rule would be read by Kubernetes as "this
// port to ANY destination" -- turning a hostname allowlist into an
// all-destinations opening. Today that reasoning is verified by asserting the
// built object has no egress rule. This asserts the consequence: the host is
// actually unreachable, and so is everything else on that port.
func TestHostOnlyRuleFailsClosed(t *testing.T) {
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	target := runningPod(t, cs, ns, "fqdn-target",
		map[string]string{"role": "target"}, []string{"nc -lk -p 8080 -e /bin/true"})

	server := serverWithEgress("srv", ns, []mcpv1alpha2.EgressRuleSpec{
		{Host: "api.example.com", Port: 8080},
	})
	np := networkpolicy.BuildNetworkPolicy(server)
	for _, rule := range np.Spec.Egress {
		if len(rule.To) == 0 && len(rule.Ports) > 0 {
			t.Fatal("builder emitted a port-only egress rule: that opens the port to every destination")
		}
	}
	applyPolicy(t, cs, np)

	if canReach(t, cs, ns, "probe-fqdn", map[string]string{networkpolicy.LabelProvider: "srv"},
		target.Status.PodIP, 8080) {
		t.Error("a host-only rule opened traffic on that port; it must fail closed")
	}
}

// TestNamespaceDefaultDenyLimitsUnregisteredPods covers the shadow-workload
// case: a pod nobody registered should reach nothing but DNS.
func TestNamespaceDefaultDenyLimitsUnregisteredPods(t *testing.T) {
	cs := clientset(t)
	ns := namespace(t, cs, map[string]string{networkpolicy.EnforceEgressLabel: "true"})

	target := runningPod(t, cs, ns, "some-target",
		map[string]string{"role": "target"}, []string{"nc -lk -p 8080 -e /bin/true"})

	applyPolicy(t, cs, networkpolicy.BuildNamespaceDefaultDenyEgress(ns))

	// No provider label: this pod matches no per-server allow policy.
	if canReach(t, cs, ns, "probe-shadow", map[string]string{"role": "shadow"},
		target.Status.PodIP, 8080) {
		t.Error("an unregistered pod reached an upstream; the namespace default-deny is not enforced")
	}
}

// TestWithoutAPolicyTrafficFlows is the control.
//
// Without it, every test above would also pass on a cluster whose CNI silently
// drops everything, or where the pods never started -- "blocked" would be
// indistinguishable from "broken". This asserts the harness can observe a
// successful connection at all.
func TestWithoutAPolicyTrafficFlows(t *testing.T) {
	cs := clientset(t)
	ns := namespace(t, cs, nil)

	target := runningPod(t, cs, ns, "open-target",
		map[string]string{"role": "target"}, []string{"nc -lk -p 8080 -e /bin/true"})

	if !canReach(t, cs, ns, "probe-open", map[string]string{"role": "client"},
		target.Status.PodIP, 8080) {
		t.Fatal("pod-to-pod traffic failed with NO policy applied -- the cluster or the harness is broken, " +
			"and every deny assertion in this file would pass for the wrong reason")
	}
}

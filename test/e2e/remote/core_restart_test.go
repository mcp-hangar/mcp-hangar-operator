//go:build e2e_remote

package remote

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

// redeliveryWindow bounds how long after the new core pod turns Ready the
// policy may take to be held and enforced again. The operator pushes when its
// pod watch sees the Ready transition, which takes seconds; the nearest other
// trigger that could re-push is the informer resync, hours away, so a pass
// inside this window is the gateway watch at work.
const redeliveryWindow = 30 * time.Second

// testL7RedeliveredAfterCoreRestart is #400: an L7 policy is enforced again
// after core restarts with no persistence backend.
//
// manifests/core.yaml configures no persistence (the chart default), so the
// new core pod comes back holding no policy, and the operator's re-delivery
// when a gateway pod turns Ready is the only thing that restores enforcement
// (mcp-hangar/mcp-hangar#1306). Core says so in the push response, which the
// operator records as L7Delivered reason DeliveredNotPersisted; that is
// asserted first, so this test cannot pass through core restoring its own copy.
//
// Selector: the operator finds gateway pods by --hangar-gateway-selector,
// default app.kubernetes.io/name=mcp-hangar, which is how the mcp-hangar chart
// labels core's pods. manifests/core.yaml carries that label on the pod
// template rather than the overlay passing --hangar-gateway-selector=app=mcp-hangar,
// so the e2e runs the operator with the default a chart install gets. Without
// the label the watch matches nothing, re-delivery is a silent no-op and this
// test fails on the l7_policy read below.
func testL7RedeliveredAfterCoreRestart(t *testing.T, cl crclient.Client, cs *kubernetes.Clientset) {
	ctx := context.Background()
	const name = "backend-egress-restart"

	// The previous subtest's policy clears core's copy for "backend" when it is
	// deleted, before its finalizer is released. Wait for that, or the clear
	// can land after this policy's push and wipe it.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		err := cl.Get(ctx, types.NamespacedName{Name: "backend-egress", Namespace: stackNamespace}, &mcpv1alpha2.MCPEgressPolicy{})
		if apierrors.IsNotFound(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCPEgressPolicy backend-egress still present a minute after its subtest: %v", err)
		}
	}

	delivered := applyBackendDenyPolicy(t, cl, name)
	if delivered.Reason != "DeliveredNotPersisted" {
		t.Fatalf("L7Delivered reason = %q, want DeliveredNotPersisted: core must report persisted=false, "+
			"or a restart would be restored by core itself and not by re-delivery: %s", delivered.Reason, delivered.Message)
	}
	before := corePodName(t, "mcp-hangar")
	assertL7Enforced(t, before, "")

	if out, err := exec.Command("kubectl", "-n", stackNamespace, "rollout", "restart", "deploy/mcp-hangar").CombinedOutput(); err != nil {
		t.Fatalf("rollout restart core: %v: %s", err, out)
	}
	if out, err := exec.Command("kubectl", "-n", stackNamespace, "rollout", "status", "deploy/mcp-hangar",
		"--timeout=5m").CombinedOutput(); err != nil {
		t.Fatalf("core rollout: %v: %s", err, out)
	}

	pod, readyAt := newReadyCorePod(t, cs, before)
	deadline := readyAt.Add(redeliveryWindow)
	t.Logf("core pod %s Ready at %s; the policy must be back by %s", pod, readyAt.Format(time.RFC3339), deadline.Format(time.RFC3339))

	body := ""
	for {
		polled := time.Now()
		body = coreGetIn(t, pod, "/api/mcp_servers/"+registeredName+"/l7_policy")
		if strings.Contains(body, l7DeniedTool) {
			if polled.After(deadline) {
				// Held, but first looked at too late to say it came back in time.
				t.Fatalf("first read the policy %s after Ready, past the %s window; cannot tell when it came back",
					polled.Sub(readyAt).Round(time.Second), redeliveryWindow)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("core pod %s holds no policy denying %q %s after it turned Ready; the operator did not "+
				"re-deliver it: %s\noperator log:\n%s", pod, l7DeniedTool, redeliveryWindow, strings.TrimSpace(body),
				operatorLogLines(t, cs, "gateway"))
		}
		time.Sleep(time.Second)
	}
	t.Logf("policy held again within %s of Ready; operator: %s", time.Since(readyAt).Round(time.Second),
		operatorLogLines(t, cs, "gateway pod became ready"))

	// The refusal is asserted on real client calls, not only on the route.
	assertL7Enforced(t, pod, " after the core restart")

	if v, ok := l7PolicyHeld(t, pod); !ok || v != 1 {
		t.Errorf("mcp_hangar_l7_policy_held{mcp_server=%q} = %v (present: %v), want 1", registeredName, v, ok)
	}

	got := &mcpv1alpha2.MCPEgressPolicy{}
	if err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: stackNamespace}, got); err != nil {
		t.Fatalf("get MCPEgressPolicy %q: %v", name, err)
	}
	if c := apimeta.FindStatusCondition(got.Status.Conditions, "L7Delivered"); c == nil ||
		c.Status != metav1.ConditionTrue || c.Reason != "DeliveredNotPersisted" {
		t.Errorf("L7Delivered after the restart = %+v, want True DeliveredNotPersisted", c)
	}

	// Later subtests address core by the first app=mcp-hangar pod.
	waitSingleCorePod(t, cs)
}

// newReadyCorePod returns the Ready core pod that replaced before, with the
// time its Ready condition turned True. The old pod may still be terminating.
func newReadyCorePod(t *testing.T, cs *kubernetes.Clientset, before string) (string, time.Time) {
	t.Helper()
	ctx := context.Background()
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=mcp-hangar"})
		if err != nil {
			continue
		}
		for _, p := range pods.Items {
			if p.Name == before || p.DeletionTimestamp != nil {
				continue
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return p.Name, c.LastTransitionTime.Time
				}
			}
		}
	}
	t.Fatal("no new Ready core pod after the restart")
	return "", time.Time{}
}

// waitSingleCorePod waits until exactly one app=mcp-hangar pod is left.
func waitSingleCorePod(t *testing.T, cs *kubernetes.Clientset) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		pods, err := cs.CoreV1().Pods(stackNamespace).List(context.Background(),
			metav1.ListOptions{LabelSelector: "app=mcp-hangar"})
		if err == nil && len(pods.Items) == 1 {
			return
		}
	}
	t.Fatal("the old core pod was not gone two minutes after the restart")
}

// l7PolicyHeld reads mcp_hangar_l7_policy_held for the backend from core's
// /metrics: a supporting check that core counts the policy it holds.
func l7PolicyHeld(t *testing.T, pod string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(coreGetIn(t, pod, "/metrics"), "\n") {
		if !strings.HasPrefix(line, "mcp_hangar_l7_policy_held{") ||
			!strings.Contains(line, `mcp_server="`+registeredName+`"`) {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Errorf("unreadable metric line %q: %v", line, err)
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// operatorLogLines returns the operator's log lines that mention substr, for
// a failure message.
func operatorLogLines(t *testing.T, cs *kubernetes.Clientset, substr string) string {
	t.Helper()
	tail := int64(2000)
	logs, err := cs.CoreV1().Pods(operatorNamespace).GetLogs(operatorPodName(t, cs),
		&corev1.PodLogOptions{TailLines: &tail}).DoRaw(context.Background())
	if err != nil {
		return "(operator logs unavailable: " + err.Error() + ")"
	}
	var lines []string
	for _, l := range strings.Split(string(logs), "\n") {
		if strings.Contains(strings.ToLower(l), substr) {
			lines = append(lines, l)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

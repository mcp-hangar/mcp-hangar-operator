//go:build e2e_remote

// Package remote verifies the mode:remote MCPServer lifecycle against a LIVE
// core -- the half route_contract_test.go disclaims about itself: "Paths only
// ... shape drift needs a smoke test against a running core." (#106)
//
// The unit tests mock the HTTP layer, so "core does not serve this route" (#91,
// #103) and "core renamed a field" are exactly what they cannot express. Here
// the assertions are made on what only a live pairing can show:
//
//   - a remote CR reaches Ready with the backend's real tool count -- an
//     assertion a dead route or a renamed consecutive_failures could not
//     satisfy, because Ready requires decoding core's actual health shape;
//   - an unregistered server yields a condition an operator can act on
//     ("not registered with core"), not a JSON decode error;
//   - with core unreachable, probing is SUSTAINED at roughly the 10s
//     errorRequeueAfter cadence (~6/min) -- not the ~168/s self-trigger loop of
//     #105, and not stalled. Asserted as a rate window, deliberately not a
//     backoff curve: no backoff exists (#107 removed the self-trigger instead).
//
// Requires the stack booted by up.sh (make e2e-remote-up): core with a server
// named "backend" registered via config, the task_upstream backend (3 tools),
// and the released operator pointed at core.
package remote

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

const (
	// stackNamespace is where up.sh deploys core and the backend, and where the
	// test creates its CRs.
	stackNamespace = "hangar-e2e"

	// operatorNamespace is where the operator overlay installs the manager.
	operatorNamespace = "mcp-hangar"

	// registeredName must match the mcp_servers key in manifests/core.yaml --
	// core keys servers by NAME, so the CR name is the join key.
	registeredName = "backend"

	// backendToolCount is what examples/task_upstream serves: long_job,
	// long_job_consent, echo. Exact on purpose -- "toolsCount > 0" would still
	// pass if core started projecting some other server's catalogue.
	backendToolCount = int32(3)

	backendEndpoint = "http://task-upstream.hangar-e2e.svc.cluster.local:8080/mcp"

	// readyTimeout bounds the Progressing->Ready wait. Generous: it covers the
	// operator's first reconcile plus core's discovery of the backend.
	readyTimeout = 4 * time.Minute

	// cadenceWindow is how long probe attempts are counted with core down.
	cadenceWindow = 2 * time.Minute

	// healthUnreachableLog is the exact line reconcileRemoteProvider logs when
	// core cannot be asked at all -- one line per probe attempt, which makes the
	// operator log a probe counter.
	healthUnreachableLog = "Could not read health from Hangar core"
)

func kubeClients(t *testing.T) (crclient.Client, *kubernetes.Clientset) {
	t.Helper()
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		path = os.Getenv("HOME") + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := mcpv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add mcp scheme: %v", err)
	}
	cl, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("controller-runtime client: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return cl, cs
}

// createRemoteCR creates a fresh mode:remote MCPServer, replacing any leftover
// from a previous run so local reruns start clean.
func createRemoteCR(t *testing.T, cl crclient.Client, name, endpoint string) {
	t.Helper()
	ctx := context.Background()

	old := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stackNamespace}}
	if err := cl.Delete(ctx, old); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete leftover %s: %v", name, err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: stackNamespace}, &mcpv1alpha2.MCPServer{})
		if apierrors.IsNotFound(err) {
			break
		}
		time.Sleep(2 * time.Second)
	}

	cr := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stackNamespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Endpoint: endpoint,
		},
	}
	if err := cl.Create(ctx, cr); err != nil {
		t.Fatalf("create MCPServer %s: %v", name, err)
	}
}

func getServer(t *testing.T, cl crclient.Client, name string) *mcpv1alpha2.MCPServer {
	t.Helper()
	srv := &mcpv1alpha2.MCPServer{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: stackNamespace}, srv); err != nil {
		t.Fatalf("get MCPServer %s: %v", name, err)
	}
	return srv
}

func TestRemoteLifecycle(t *testing.T) {
	cl, cs := kubeClients(t)

	// Subtests share cluster state and their order is load-bearing: the cadence
	// test kills core, so it must run last.
	t.Run("RegisteredServerReachesReady", func(t *testing.T) {
		testReady(t, cl)
	})
	t.Run("UnregisteredServerSaysSo", func(t *testing.T) {
		testNotRegistered(t, cl)
	})
	t.Run("CoreUnreachableProbeCadence", func(t *testing.T) {
		testProbeCadence(t, cl, cs)
	})
}

// testReady is the assertion that could not have been satisfied by a dead
// route or a drifted response shape: Ready requires the operator to decode
// core's live health response and read consecutive_failures == 0, and the tool
// count requires the live tools response to carry the backend's catalogue.
func testReady(t *testing.T, cl crclient.Client) {
	createRemoteCR(t, cl, registeredName, backendEndpoint)

	deadline := time.Now().Add(readyTimeout)
	var last *mcpv1alpha2.MCPServer
	for time.Now().Before(deadline) {
		last = getServer(t, cl, registeredName)
		ready := apimeta.FindStatusCondition(last.Status.Conditions, "Ready")
		if ready != nil && ready.Status == metav1.ConditionTrue && last.Status.ToolsCount == backendToolCount {
			break
		}
		time.Sleep(3 * time.Second)
	}

	ready := apimeta.FindStatusCondition(last.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("MCPServer %q never reached Ready=True; status: %+v", registeredName, last.Status)
	}
	if ready.Reason != "EndpointHealthy" {
		t.Errorf("Ready reason = %q, want EndpointHealthy (the live-core path)", ready.Reason)
	}
	// The Progressing condition is set on the first reconcile pass and never
	// removed, so its presence next to Ready=True is the record of the
	// Progressing -> Ready transition. (Both can land in one status write --
	// the first reconcile may complete the health check immediately -- so
	// polling for a Progressing-only intermediate state would be a race.)
	if apimeta.FindStatusCondition(last.Status.Conditions, "Progressing") == nil {
		t.Error("no Progressing condition recorded; the CR skipped the reconcile path that sets it")
	}
	if last.Status.State != mcpv1alpha2.MCPServerStateReady {
		t.Errorf("status.state = %q, want Ready", last.Status.State)
	}
	if last.Status.ToolsCount != backendToolCount {
		t.Errorf("status.toolsCount = %d, want %d (task_upstream serves long_job, long_job_consent, echo)",
			last.Status.ToolsCount, backendToolCount)
	}
}

// testNotRegistered pins the 404 path: the condition must carry the message an
// operator can act on, not the JSON decode error #103 shipped.
func testNotRegistered(t *testing.T, cl crclient.Client) {
	createRemoteCR(t, cl, "ghost", "http://ghost.hangar-e2e.svc.cluster.local:9999/mcp")

	deadline := time.Now().Add(2 * time.Minute)
	var degraded *metav1.Condition
	for time.Now().Before(deadline) {
		srv := getServer(t, cl, "ghost")
		degraded = apimeta.FindStatusCondition(srv.Status.Conditions, "Degraded")
		if degraded != nil && degraded.Status == metav1.ConditionTrue {
			break
		}
		time.Sleep(3 * time.Second)
	}

	if degraded == nil || degraded.Status != metav1.ConditionTrue {
		t.Fatal("MCPServer \"ghost\" never went Degraded; core accepted a server nobody registered")
	}
	if !strings.Contains(degraded.Message, "not registered with core") {
		t.Errorf("Degraded message = %q, want it to say \"not registered with core\"", degraded.Message)
	}
	// The #103 regression shape: a 404 body fed to the JSON decoder.
	for _, fragment := range []string{"failed to decode", "invalid character"} {
		if strings.Contains(degraded.Message, fragment) {
			t.Errorf("Degraded message leaks a decode error (%q): %q", fragment, degraded.Message)
		}
	}
}

// testProbeCadence scales core to zero and counts probe attempts in the
// operator's log. Both CRs from the earlier subtests are on the 10s
// errorRequeueAfter cadence once core is gone, and each attempt logs
// healthUnreachableLog exactly once -- so the operator log IS the probe counter.
//
// Bounds, not equality, and the arithmetic is deliberately loose at the bottom:
// a probe cycle is the 10s requeue plus the client's in-call retries (~3.5s),
// so each CR contributes ~4-6 per minute and two CRs put the expected count for
// a 2-minute window around 18-24. The floor of 4 still separates "sustained"
// from "stalled" even if connections to the endpointless Service hang rather
// than being refused; the ceiling of 60 catches the #105 shape, which was
// ~168/s -- twenty thousand per window, not sixty.
func testProbeCadence(t *testing.T, cl crclient.Client, cs *kubernetes.Clientset) {
	ctx := context.Background()

	// Scale core down and wait until no pod is left; until then, probes can
	// still succeed and would not log.
	core := &appsv1.Deployment{}
	if err := cl.Get(ctx, types.NamespacedName{Name: "mcp-hangar", Namespace: stackNamespace}, core); err != nil {
		t.Fatalf("get core deployment: %v", err)
	}
	zero := int32(0)
	core.Spec.Replicas = &zero
	if err := cl.Update(ctx, core); err != nil {
		t.Fatalf("scale core to 0: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=mcp-hangar"})
		if err == nil && len(pods.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core pods never terminated after scaling to 0")
		}
		time.Sleep(3 * time.Second)
	}

	operatorPod := operatorPodName(t, cs)
	windowStart := metav1.Now()
	time.Sleep(cadenceWindow)

	logs, err := cs.CoreV1().Pods(operatorNamespace).
		GetLogs(operatorPod, &corev1.PodLogOptions{SinceTime: &windowStart}).
		DoRaw(ctx)
	if err != nil {
		t.Fatalf("operator logs: %v", err)
	}
	probes := strings.Count(string(logs), healthUnreachableLog)

	perMinute := float64(probes) / cadenceWindow.Minutes()
	t.Logf("core-unreachable probe attempts in %s: %d (%.1f/min)", cadenceWindow, probes, perMinute)
	if probes < 4 {
		t.Errorf("only %d probe attempts in %s; probing stalled -- recovery would go undetected", probes, cadenceWindow)
	}
	if probes > 60 {
		t.Errorf("%d probe attempts in %s; that is a reconcile storm, not the sustained ~6/min cadence", probes, cadenceWindow)
	}

	// The counter itself must not explode either: with core unreachable there
	// is nothing to mirror, so consecutiveFailures stays put instead of
	// counting probe attempts (the #105 counter hit 1.8 million).
	for _, name := range []string{registeredName, "ghost"} {
		srv := getServer(t, cl, name)
		if srv.Status.ConsecutiveFailures > 10 {
			t.Errorf("MCPServer %q consecutiveFailures = %d with core unreachable; the counter is counting probes again",
				name, srv.Status.ConsecutiveFailures)
		}
	}
}

func operatorPodName(t *testing.T, cs *kubernetes.Clientset) string {
	t.Helper()
	pods, err := cs.CoreV1().Pods(operatorNamespace).List(context.Background(),
		metav1.ListOptions{LabelSelector: "control-plane=controller-manager"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("find operator pod: err=%v, found=%d", err, len(pods.Items))
	}
	if len(pods.Items) > 1 {
		// One replica is what the overlay deploys; two means a rollout is in
		// flight and the log count would be split across pods.
		t.Fatalf("expected exactly one operator pod, found %d", len(pods.Items))
	}
	return pods.Items[0].Name
}

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
//   - a remote CR naming a server core does not hold is registered by the
//     operator, core reaches its in-cluster Service through the trusted
//     network, and deleting the CR removes the server from core (#164);
//   - an endpoint outside core's ssrf.trusted_networks yields a condition that
//     names the setting, not a bare 400, and a mistyped Service is never
//     Ready (#164);
//   - an MCPEgressPolicy's L7 half is held by core, read back from core's own
//     l7_policy route, not only reported on the CR (#208);
//   - deleting a remote CR that mirrors a config-defined server leaves that
//     server in core (#202);
//   - a container-mode CR runs a pod the operator stamps with the discovery
//     annotations, core's kubernetes discovery registers it, and the CR reaches
//     Ready with the backend's tool count; deleting the CR takes it out (#294);
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
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
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
	t.Run("UnknownServerIsRegistered", func(t *testing.T) {
		testRegistersUnknownServer(t, cl)
	})
	t.Run("UntrustedEndpointSaysSo", func(t *testing.T) {
		testUntrustedEndpoint(t, cl)
	})
	t.Run("MistypedEndpointIsNotReady", func(t *testing.T) {
		testMistypedEndpoint(t, cl)
	})
	t.Run("EgressPolicyL7ReachesCore", func(t *testing.T) {
		testL7Delivered(t, cl)
	})
	t.Run("DeletingAMirrorLeavesCoreAlone", func(t *testing.T) {
		testDeleteMirror(t, cl)
	})
	t.Run("ContainerModeThroughDiscovery", func(t *testing.T) {
		testContainerMode(t, cl, cs)
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

// operatorRegisteredName is a remote CR core's config does not declare.
const operatorRegisteredName = "crd-declared"

// testRegistersUnknownServer is #164: the CR alone puts the server in core.
// The endpoint is the backend's Service, a ClusterIP in the kind service range
// core trusts (manifests/core.yaml, ssrf.trusted_networks); core then starts it
// and lists its tools, which proves the connect-time SSRF guard let it through,
// not only the registration check.
func testRegistersUnknownServer(t *testing.T, cl crclient.Client) {
	createRemoteCR(t, cl, operatorRegisteredName, backendEndpoint)

	deadline := time.Now().Add(readyTimeout)
	var last *mcpv1alpha2.MCPServer
	for time.Now().Before(deadline) {
		last = getServer(t, cl, operatorRegisteredName)
		if c := apimeta.FindStatusCondition(last.Status.Conditions, "Ready"); c != nil && c.Status == metav1.ConditionTrue {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if c := apimeta.FindStatusCondition(last.Status.Conditions, "Ready"); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("MCPServer %q never reached Ready=True; status: %+v\ncore log:\n%s",
			operatorRegisteredName, last.Status, coreLogLines("ssrf"))
	}
	claim := &coordinationv1.Lease{}
	claimKey := types.NamespacedName{Name: "mcp-server-" + operatorRegisteredName, Namespace: operatorNamespace}
	if err := cl.Get(context.Background(), claimKey, claim); err != nil {
		t.Fatalf("the operator's claim on %q: %v", operatorRegisteredName, err)
	}
	if h := claim.Spec.HolderIdentity; h == nil || !strings.HasPrefix(*h, stackNamespace+"/"+operatorRegisteredName+"/") {
		t.Errorf("claim holder = %v, want %s/%s/<uid>", h, stackNamespace, operatorRegisteredName)
	}

	// The operator started it before reporting Ready, so core already lists
	// the backend's tools.
	if tools := coreGet(t, "/api/mcp_servers/"+operatorRegisteredName+"/tools"); !strings.Contains(tools, "long_job") {
		t.Errorf("core did not list the backend's tools for %q (the connect-time guard refused it?): %s\ncore log:\n%s",
			operatorRegisteredName, tools, coreLogLines("ssrf"))
	}

	ctx := context.Background()
	if err := cl.Delete(ctx, &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{
		Name: operatorRegisteredName, Namespace: stackNamespace}}); err != nil {
		t.Fatalf("delete MCPServer %q: %v", operatorRegisteredName, err)
	}
	waitGone(t, cl, operatorRegisteredName)
	if body := coreGet(t, "/api/mcp_servers/"+operatorRegisteredName); !strings.HasPrefix(body, "HTTP 404") {
		t.Errorf("core still holds %q after its CR was deleted: %s", operatorRegisteredName, strings.TrimSpace(body))
	}
	if err := cl.Get(context.Background(), claimKey, &coordinationv1.Lease{}); !apierrors.IsNotFound(err) {
		t.Errorf("the claim on %q outlived its CR: %v", operatorRegisteredName, err)
	}
}

// testMistypedEndpoint registers a Service that does not exist. Core takes the
// registration -- a name that does not resolve is not an address to refuse --
// and reports a server nobody has called as healthy; the CR must still not
// claim Ready, because the operator's start of it fails.
func testMistypedEndpoint(t *testing.T, cl crclient.Client) {
	const name = "mistyped"
	createRemoteCR(t, cl, name, "http://task-upstraem.hangar-e2e.svc.cluster.local:8080/mcp")

	deadline := time.Now().Add(2 * time.Minute)
	var srv *mcpv1alpha2.MCPServer
	for time.Now().Before(deadline) {
		srv = getServer(t, cl, name)
		if c := apimeta.FindStatusCondition(srv.Status.Conditions, "Degraded"); c != nil && c.Reason == "StartFailed" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if c := apimeta.FindStatusCondition(srv.Status.Conditions, "Degraded"); c == nil || c.Reason != "StartFailed" {
		t.Errorf("MCPServer %q with a mistyped Service never reported StartFailed; status: %+v", name, srv.Status)
	}
	if c := apimeta.FindStatusCondition(srv.Status.Conditions, "Ready"); c != nil && c.Status == metav1.ConditionTrue {
		t.Errorf("MCPServer %q reached Ready with an endpoint nothing answers", name)
	}

	if err := cl.Delete(context.Background(), &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: stackNamespace}}); err != nil {
		t.Fatalf("delete MCPServer %q: %v", name, err)
	}
	waitGone(t, cl, name)
}

// testUntrustedEndpoint registers an endpoint in the pod range, which core does
// not trust: the CR must say which core setting to change.
func testUntrustedEndpoint(t *testing.T, cl crclient.Client) {
	const name = "untrusted"
	createRemoteCR(t, cl, name, "http://10.244.0.250:9999/mcp")

	deadline := time.Now().Add(2 * time.Minute)
	var degraded *metav1.Condition
	for time.Now().Before(deadline) {
		degraded = apimeta.FindStatusCondition(getServer(t, cl, name).Status.Conditions, "Degraded")
		if degraded != nil && degraded.Status == metav1.ConditionTrue {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if degraded == nil || degraded.Status != metav1.ConditionTrue {
		t.Fatalf("MCPServer %q never went Degraded; core accepted an endpoint outside its trusted networks", name)
	}
	if degraded.Reason != "EndpointNotTrusted" || !strings.Contains(degraded.Message, "ssrf.trusted_networks") {
		t.Errorf("Degraded = %s: %q, want EndpointNotTrusted naming ssrf.trusted_networks", degraded.Reason, degraded.Message)
	}

	if err := cl.Delete(context.Background(), &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: stackNamespace}}); err != nil {
		t.Fatalf("delete MCPServer %q: %v", name, err)
	}
	waitGone(t, cl, name)
}

// waitGone waits for a deleted CR's finalizer to be released.
func waitGone(t *testing.T, cl crclient.Client, name string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: stackNamespace}, &mcpv1alpha2.MCPServer{})
		if apierrors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCPServer %q still present 60s after delete; the finalizer was not released", name)
		}
		time.Sleep(2 * time.Second)
	}
}

// testProbeCadence scales core to zero and counts probe attempts in the
// operator's log: each attempt logs healthUnreachableLog exactly once, so the
// operator log IS the probe counter.
//
// The count is driven by "cadence", a CR created once core is gone: its first
// reconcile cannot ask core, which puts it on the 10s errorRequeueAfter
// cadence. "backend" is Ready and therefore on the 5-minute readyRequeueAfter,
// so it contributes at most one attempt inside the window before dropping to
// the error cadence too.
//
// Bounds, not equality, and deliberately loose at the bottom: a probe cycle is
// the 10s requeue plus the client's in-call retries (~3.5s), so a 2-minute
// window holds roughly 9. The floor of 4 still separates "sustained" from
// "stalled" even if connections to the endpointless Service hang rather than
// being refused; the ceiling of 60 catches the #105 shape, which was ~168/s --
// twenty thousand per window, not sixty. Measured on the first green run: 18.
// cadenceName is the CR the cadence test creates with core down.
const cadenceName = "cadence"

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

	createRemoteCR(t, cl, cadenceName, "http://cadence.hangar-e2e.svc.cluster.local:9999/mcp")

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
	for _, name := range []string{registeredName, cadenceName} {
		srv := getServer(t, cl, name)
		if srv.Status.ConsecutiveFailures > 10 {
			t.Errorf("MCPServer %q consecutiveFailures = %d with core unreachable; the counter is counting probes again",
				name, srv.Status.ConsecutiveFailures)
		}
	}
}

// l7DeniedTool is a tool only this test's policy denies. Finding it in core's
// copy of the policy proves the operator delivered this policy, not that some
// earlier one happens to be there.
const l7DeniedTool = "long_job_consent"

// testL7Delivered creates an MCPEgressPolicy for "backend" and reads the policy
// back from core. The CR's L7Delivered condition is the operator's own claim;
// core's GET /api/mcp_servers/backend/l7_policy is the evidence. If
// pushL7Policy were a no-op the condition would never be set and core would
// answer 404 no_l7_policy, so both assertions fail (#208).
func testL7Delivered(t *testing.T, cl crclient.Client) {
	ctx := context.Background()
	name := "backend-egress"

	old := &mcpv1alpha2.MCPEgressPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stackNamespace}}
	if err := cl.Delete(ctx, old); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete leftover policy: %v", err)
	}
	policy := &mcpv1alpha2.MCPEgressPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: stackNamespace},
		Spec: mcpv1alpha2.MCPEgressPolicySpec{
			TargetRef:     mcpv1alpha2.EgressTargetRef{Kind: "MCPServer", Name: registeredName},
			DefaultAction: mcpv1alpha2.EgressActionAllow,
			Upstreams: []mcpv1alpha2.UpstreamRule{{
				Name:  "task-upstream",
				Match: mcpv1alpha2.UpstreamMatch{Host: "task-upstream.hangar-e2e.svc.cluster.local"},
				Tools: &mcpv1alpha2.ToolRules{Deny: []string{l7DeniedTool}},
			}},
		},
	}
	// A policy left by an earlier local run may still be terminating.
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := cl.Create(ctx, policy)
		if err == nil {
			break
		}
		if !apierrors.IsAlreadyExists(err) || time.Now().After(deadline) {
			t.Fatalf("create MCPEgressPolicy: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	t.Cleanup(func() { _ = cl.Delete(context.Background(), policy) })

	var delivered *metav1.Condition
	deadline = time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		got := &mcpv1alpha2.MCPEgressPolicy{}
		if err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: stackNamespace}, got); err == nil {
			delivered = apimeta.FindStatusCondition(got.Status.Conditions, "L7Delivered")
			if delivered != nil && delivered.Status == metav1.ConditionTrue {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	if delivered == nil || delivered.Status != metav1.ConditionTrue {
		t.Fatalf("MCPEgressPolicy %q never reported L7Delivered=True; last condition: %+v", name, delivered)
	}

	body := coreGet(t, "/api/mcp_servers/"+registeredName+"/l7_policy")
	if !strings.Contains(body, l7DeniedTool) {
		t.Fatalf("core's l7_policy for %q does not deny %q, so the operator's claim is not what core holds: %s",
			registeredName, l7DeniedTool, body)
	}
}

// testDeleteMirror deletes the "backend" CR, which mirrors a server core has
// from its config, and asks core whether the server is still there. The
// operator used to send DELETE /api/mcp_servers/backend on every CR deletion,
// which took the config-defined server out of core until a reload (#202). The
// CR is recreated afterwards: the cadence test reads it.
func testDeleteMirror(t *testing.T, cl crclient.Client) {
	ctx := context.Background()
	cr := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: registeredName, Namespace: stackNamespace}}
	if err := cl.Delete(ctx, cr); err != nil {
		t.Fatalf("delete MCPServer %q: %v", registeredName, err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := cl.Get(ctx, types.NamespacedName{Name: registeredName, Namespace: stackNamespace}, &mcpv1alpha2.MCPServer{})
		if apierrors.IsNotFound(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCPServer %q still present 60s after delete; the finalizer was not released", registeredName)
		}
		time.Sleep(2 * time.Second)
	}

	if body := coreGet(t, "/api/mcp_servers/"+registeredName); strings.HasPrefix(body, "HTTP ") {
		t.Errorf("core lost %q when its mirror CR was deleted: GET /api/mcp_servers/%s answered %s",
			registeredName, registeredName, strings.TrimSpace(body))
	}

	testReady(t, cl)
}

// containerName is the container-mode CR; core knows it only by discovering
// its pod.
const containerName = "boxed"

// testContainerMode drives a container-mode MCPServer end to end (#294). The
// operator runs the task_upstream image as the CR's pod and stamps the
// discovery annotations on it (pkg/provider/builder.go); core's kubernetes
// discovery source reads them and registers the pod as a server; the CR then
// reaches Ready with the backend's three tools. If the builder stopped
// stamping the annotations, core would never hear of the server and the CR
// would never be Ready.
func testContainerMode(t *testing.T, cl crclient.Client, cs *kubernetes.Clientset) {
	ctx := context.Background()
	nonRoot := int64(1000)
	cr := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: containerName, Namespace: stackNamespace},
		Spec: mcpv1alpha2.MCPServerSpec{
			Mode:  mcpv1alpha2.MCPServerModeContainer,
			// From the local registry up.sh runs: a mutable tag is pulled
			// with imagePullPolicy Always, so a kind-loaded image is not enough.
			Image: "localhost:5001/mcp-task-upstream:e2e",
			Env: []corev1.EnvVar{
				{Name: "MCP_HOST", Value: "0.0.0.0"},
				{Name: "MCP_PORT", Value: "8080"},
				{Name: "MCP_HTTP_PATH", Value: "/mcp"},
			},
			// The image has no USER; the operator's pod defaults are non-root.
			ContainerSecurityContext: &corev1.SecurityContext{RunAsUser: &nonRoot},
		},
	}
	old := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: containerName, Namespace: stackNamespace}}
	_ = cl.Delete(ctx, old)
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := cl.Create(ctx, cr)
		if err == nil {
			break
		}
		if !apierrors.IsAlreadyExists(err) || time.Now().After(deadline) {
			t.Fatalf("create container-mode MCPServer: %v", err)
		}
		time.Sleep(2 * time.Second)
	}

	// Core registers a discovered server cold, as config does "backend"; up.sh
	// starts that one, and this starts the discovered one once core has it, so
	// its tools are listed.
	deadline = time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if !strings.HasPrefix(coreGet(t, "/api/mcp_servers/"+containerName), "HTTP ") {
			corePost(t, "/api/mcp_servers/"+containerName+"/start")
			break
		}
		time.Sleep(3 * time.Second)
	}

	var last *mcpv1alpha2.MCPServer
	deadline = time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		last = getServer(t, cl, containerName)
		ready := apimeta.FindStatusCondition(last.Status.Conditions, "Ready")
		if ready != nil && ready.Status == metav1.ConditionTrue && last.Status.ToolsCount == backendToolCount {
			break
		}
		time.Sleep(3 * time.Second)
	}
	pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "mcp-hangar.io/provider=" + containerName})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("no pod for the container-mode CR: err=%v; status: %+v", err, last.Status)
	}
	pod := pods.Items[0]
	if pod.Annotations["mcp-hangar.io/enabled"] != "true" || pod.Annotations["mcp-hangar.io/name"] != containerName {
		t.Errorf("the pod lacks the discovery annotations core reads: %v", pod.Annotations)
	}
	ready := apimeta.FindStatusCondition(last.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || last.Status.ToolsCount != backendToolCount {
		var waiting []string
		for _, s := range pod.Status.ContainerStatuses {
			if s.State.Waiting != nil {
				waiting = append(waiting, s.State.Waiting.Reason+": "+s.State.Waiting.Message)
			}
		}
		t.Fatalf("container-mode MCPServer %q never reached Ready with %d tools; pod phase %s %v; status: %+v; core: %s\ncore discovery log:\n%s",
			containerName, backendToolCount, pod.Status.Phase, waiting, last.Status,
			strings.TrimSpace(coreGet(t, "/api/mcp_servers/"+containerName)), coreLogLines("discover"))
	}
	if body := coreGet(t, "/api/mcp_servers/"+containerName); strings.HasPrefix(body, "HTTP ") {
		t.Errorf("core does not list %q although the CR is Ready: %s", containerName, strings.TrimSpace(body))
	}

	// Deleting the CR takes the server out of core: the operator deregisters
	// what its pod put there (#202), and the pod is gone for discovery too.
	if err := cl.Delete(ctx, cr); err != nil {
		t.Fatalf("delete container-mode MCPServer: %v", err)
	}
	deadline = time.Now().Add(2 * time.Minute)
	body := ""
	for time.Now().Before(deadline) {
		body = coreGet(t, "/api/mcp_servers/"+containerName)
		if strings.HasPrefix(body, "HTTP 404") {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Errorf("core still has %q two minutes after its CR was deleted: %s", containerName, strings.TrimSpace(body))
}

// coreLogLines returns core's log lines that mention substr, for a failure
// message: the workflow's diagnostics do not collect core's log.
func coreLogLines(substr string) string {
	out, err := exec.Command("kubectl", "-n", stackNamespace, "logs", "-l", "app=mcp-hangar", "--tail=2000").CombinedOutput()
	if err != nil {
		return "(core logs unavailable: " + err.Error() + ")"
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(strings.ToLower(l), substr) {
			lines = append(lines, l)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}

// corePost runs a body-less POST from inside the core pod, as coreGet does a GET.
func corePost(t *testing.T, path string) string {
	t.Helper()
	pod, err := exec.Command("kubectl", "-n", stackNamespace, "get", "pod", "-l", "app=mcp-hangar",
		"-o", "jsonpath={.items[0].metadata.name}").Output()
	if err != nil {
		t.Fatalf("find core pod: %v", err)
	}
	script := `import sys, urllib.request, urllib.error
req = urllib.request.Request("http://localhost:8080" + sys.argv[1], method="POST")
try:
    print(urllib.request.urlopen(req, timeout=60).read().decode())
except urllib.error.HTTPError as e:
    print("HTTP %d: %s" % (e.code, e.read().decode()))
`
	out, err := exec.Command("kubectl", "-n", stackNamespace, "exec", string(pod), "--",
		"python3", "-c", script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("POST %s in core: %v: %s", path, err, out)
	}
	t.Logf("POST %s: %s", path, strings.TrimSpace(string(out)))
	return string(out)
}

// coreGet runs a GET from inside the core pod. The API port is not published
// outside the cluster, and core rejects a Host it does not trust, so up.sh
// reaches it the same way: the image ships python3, not curl.
func coreGet(t *testing.T, path string) string {
	t.Helper()
	pod, err := exec.Command("kubectl", "-n", stackNamespace, "get", "pod", "-l", "app=mcp-hangar",
		"-o", "jsonpath={.items[0].metadata.name}").Output()
	if err != nil {
		t.Fatalf("find core pod: %v", err)
	}
	script := `import sys, urllib.request, urllib.error
try:
    print(urllib.request.urlopen("http://localhost:8080" + sys.argv[1], timeout=30).read().decode())
except urllib.error.HTTPError as e:
    print("HTTP %d: %s" % (e.code, e.read().decode()))
`
	out, err := exec.Command("kubectl", "-n", stackNamespace, "exec", string(pod), "--",
		"python3", "-c", script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("GET %s in core: %v: %s", path, err, out)
	}
	return string(out)
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

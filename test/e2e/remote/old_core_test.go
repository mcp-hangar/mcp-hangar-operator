//go:build e2e_remote

package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
)

const (
	coreURL      = "http://mcp-hangar.hangar-e2e.svc.cluster.local:8080"
	oldCoreURL   = "http://mcp-hangar-old.hangar-e2e.svc.cluster.local:8080"
	oldCoreTag   = "2.26.0" // manifests/core-old.yaml
	oldCoreCR    = "old-core-remote"
	operatorName = "mcp-hangar-operator-controller-manager"
)

// testOlderCoreRefused points the operator at core 2.26.0, older than
// hangar.MinCoreVersion (#330). The refusal is one Warning Event regarding the
// operator's Pod; an MCPServer reports Ready=False with reason CoreIncompatible
// and is not Degraded, and core is not asked to register it. Before #330 the
// same CR went Degraded with EndpointNotTrusted, one per server.
func testOlderCoreRefused(t *testing.T, cl crclient.Client, cs *kubernetes.Clientset) {
	setHangarURL(t, cl, cs, oldCoreURL)
	defer func() {
		setHangarURL(t, cl, cs, coreURL)
		waitOperatorEvent(t, cs, "CoreCompatible", "meets the minimum")
	}()
	waitOperatorEvent(t, cs, "CoreIncompatible", "core "+oldCoreTag+" is older than")

	createRemoteCR(t, cl, oldCoreCR, backendEndpoint)
	defer func() {
		_ = cl.Delete(context.Background(), &mcpv1alpha2.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: oldCoreCR, Namespace: stackNamespace},
		})
		waitGone(t, cl, oldCoreCR)
	}()

	refused := false
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		for _, name := range []string{oldCoreCR, registeredName} {
			srv := getServer(t, cl, name)
			if c := apimeta.FindStatusCondition(srv.Status.Conditions, "Degraded"); c != nil && c.Status == metav1.ConditionTrue {
				t.Fatalf("MCPServer %q went Degraded against the older core: %s: %s", name, c.Reason, c.Message)
			}
		}
		ready := apimeta.FindStatusCondition(getServer(t, cl, oldCoreCR).Status.Conditions, "Ready")
		refused = refused || (ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == "CoreIncompatible")
	}
	if !refused {
		t.Fatalf("MCPServer %q never reported Ready=False reason CoreIncompatible: %+v",
			oldCoreCR, getServer(t, cl, oldCoreCR).Status.Conditions)
	}
	if body := coreGetFrom(t, "mcp-hangar-old", "/api/mcp_servers/"+oldCoreCR); !strings.Contains(body, "HTTP 404") {
		t.Errorf("the older core holds %q; the operator registered it anyway: %s", oldCoreCR, body)
	}
}

// setHangarURL rolls the operator onto another core and waits until its one
// pod is the new one.
func setHangarURL(t *testing.T, cl crclient.Client, cs *kubernetes.Clientset, url string) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Name: operatorName, Namespace: operatorNamespace}
	dep := &appsv1.Deployment{}
	if err := cl.Get(ctx, key, dep); err != nil {
		t.Fatalf("get operator deployment: %v", err)
	}
	args := dep.Spec.Template.Spec.Containers[0].Args
	for i, a := range args {
		if strings.HasPrefix(a, "--hangar-url=") {
			args[i] = "--hangar-url=" + url
		}
	}
	if err := cl.Update(ctx, dep); err != nil {
		t.Fatalf("point the operator at %s: %v", url, err)
	}
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		if err := cl.Get(ctx, key, dep); err != nil {
			continue
		}
		pods, err := cs.CoreV1().Pods(operatorNamespace).List(ctx,
			metav1.ListOptions{LabelSelector: "control-plane=controller-manager"})
		if err == nil && dep.Status.ObservedGeneration >= dep.Generation && dep.Status.UpdatedReplicas == 1 &&
			dep.Status.AvailableReplicas == 1 && dep.Status.Replicas == 1 && len(pods.Items) == 1 {
			return
		}
	}
	t.Fatalf("the operator did not roll out onto %s", url)
}

// waitOperatorEvent waits for an Event regarding the operator's current Pod.
func waitOperatorEvent(t *testing.T, cs *kubernetes.Clientset, reason, note string) {
	t.Helper()
	pod := operatorPodName(t, cs)
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		evs, err := cs.EventsV1().Events(operatorNamespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, e := range evs.Items {
			if e.Regarding.Kind == "Pod" && e.Regarding.Name == pod && e.Reason == reason && strings.Contains(e.Note, note) {
				t.Logf("operator Event %s: %s", reason, e.Note)
				return
			}
		}
	}
	t.Fatalf("no %s Event regarding operator pod %s", reason, pod)
}

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/provider"
)

// Container-mode servers run as a Deployment (#221): kubelet restarts a
// crashed server and the operator reports it. These replace the #209 tests of
// the operator's own restart counter and backoff, which the Deployment made
// redundant.

func restartServer(name string, generation int64) *mcpv1alpha2.MCPServer {
	return &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("uid-" + name),
			Generation: generation, Finalizers: []string{finalizerName}},
		Spec: mcpv1alpha2.MCPServerSpec{Mode: mcpv1alpha2.MCPServerModeContainer, Image: "busybox:latest"},
	}
}

// deploymentPod is a pod the server's Deployment made: the selector labels,
// a generated name, no owner reference to the MCPServer.
func deploymentPod(server string, status corev1.PodStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-" + server + "-7d9c5-abcde", Namespace: "default",
			Labels:            map[string]string{provider.LabelProvider: server, provider.LabelManagedBy: provider.DefaultManagerName},
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: "busybox:latest"}}},
		Status: status,
	}
}

func crashLooping(restarts int32) corev1.PodStatus {
	return corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "provider", RestartCount: restarts,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 40s"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}}}
}

func getDeployment(t *testing.T, r *MCPServerReconciler, server string) (*appsv1.Deployment, error) {
	t.Helper()
	d := &appsv1.Deployment{}
	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-" + server, Namespace: "default"}, d)
	return d, err
}

// The first reconcile creates the Deployment, not a pod.
func TestMCPServer_ContainerMode_CreatesADeployment(t *testing.T) {
	r := newMCPServerReconciler(restartServer("dep", 1))

	reconcileMCPServer(t, r, "dep", "default")

	d, err := getDeployment(t, r, "dep")
	require.NoError(t, err)
	assert.Equal(t, int32(1), *d.Spec.Replicas)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, d.Spec.Strategy.Type)
	out := getMCPServer(t, r, "dep", "default")
	assert.True(t, metav1.IsControlledBy(d, out))
	assert.Equal(t, mcpv1alpha2.MCPServerStateInitializing, out.Status.State)
}

// A pod an older operator created directly would run beside the Deployment's
// pod under the same mcp-hangar.io/name, a duplicate to core. It is removed.
func TestMCPServer_ContainerMode_RemovesTheLegacyPod(t *testing.T) {
	srv := restartServer("legacy", 1)
	legacy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mcp-provider-legacy", Namespace: "default",
			Labels: map[string]string{provider.LabelProvider: "legacy", provider.LabelManagedBy: provider.DefaultManagerName},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: mcpv1alpha2.GroupVersion.String(), Kind: "MCPServer", Name: "legacy",
				UID: srv.UID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "provider", Image: "busybox:latest"}}},
	}
	r := newMCPServerReconciler(srv, legacy)

	reconcileMCPServer(t, r, "legacy", "default")

	err := r.Get(context.Background(), types.NamespacedName{Name: "mcp-provider-legacy", Namespace: "default"}, &corev1.Pod{})
	assert.True(t, apierrors.IsNotFound(err), "the legacy pod is deleted")
	_, err = getDeployment(t, r, "legacy")
	assert.NoError(t, err)
}

// A crash loop is reported once per change, with the restart count as the
// failure count, capped.
func TestMCPServer_CrashLoop_IsDeadWithTheRestartCount(t *testing.T) {
	r := newMCPServerReconciler(restartServer("crash", 1), deploymentPod("crash", crashLooping(9)))
	rec := events.NewFakeRecorder(20)
	r.Recorder = rec

	reconcileMCPServer(t, r, "crash", "default")
	reconcileMCPServer(t, r, "crash", "default")

	out := getMCPServer(t, r, "crash", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDead, out.Status.State)
	assert.Equal(t, maxConsecutiveFailures, out.Status.ConsecutiveFailures, "capped")
	ready := getCondition(out.Status.Conditions, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, "CrashLoopBackOff", ready.Reason)
	assert.Equal(t, "mcp-provider-crash-7d9c5-abcde", out.Status.PodName)
	close(rec.Events)
	warnings := 0
	for e := range rec.Events {
		if strings.HasPrefix(e, "Warning") {
			warnings++
		}
	}
	assert.Equal(t, 1, warnings, "one Warning for the crash loop, not one per reconcile")
}

// An image that cannot be pulled is reported, not left Initializing forever.
func TestMCPServer_ImagePullBackOff_IsDead(t *testing.T) {
	status := corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "provider",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}},
	}}}
	r := newMCPServerReconciler(restartServer("pull", 1), deploymentPod("pull", status))

	reconcileMCPServer(t, r, "pull", "default")

	out := getMCPServer(t, r, "pull", "default")
	assert.Equal(t, mcpv1alpha2.MCPServerStateDead, out.Status.State)
	assert.Equal(t, "ImagePullBackOff", getCondition(out.Status.Conditions, ConditionReady).Reason)
}

// A spec change rolls the Deployment out and starts the failure count over; a
// reconcile with nothing changed writes nothing.
func TestMCPServer_SpecChange_RollsOutAndResetsTheFailureCount(t *testing.T) {
	r := newMCPServerReconciler(restartServer("respec", 1))
	reconcileMCPServer(t, r, "respec", "default")
	d, err := getDeployment(t, r, "respec")
	require.NoError(t, err)
	unchanged := d.ResourceVersion

	reconcileMCPServer(t, r, "respec", "default")
	d, err = getDeployment(t, r, "respec")
	require.NoError(t, err)
	assert.Equal(t, unchanged, d.ResourceVersion, "a steady reconcile does not rewrite the Deployment")

	cur := getMCPServer(t, r, "respec", "default")
	cur.Spec.Image = "busybox:1.36"
	require.NoError(t, r.Update(context.Background(), cur))
	cur = getMCPServer(t, r, "respec", "default")
	cur.Status.ConsecutiveFailures = 4
	require.NoError(t, r.Status().Update(context.Background(), cur))

	reconcileMCPServer(t, r, "respec", "default")

	d, err = getDeployment(t, r, "respec")
	require.NoError(t, err)
	assert.Equal(t, "busybox:1.36", d.Spec.Template.Spec.Containers[0].Image)
	assert.Zero(t, getMCPServer(t, r, "respec", "default").Status.ConsecutiveFailures)
}

// replicas: 0 scales the Deployment to zero and reports Cold.
func TestMCPServer_Cold_ScalesTheDeploymentToZero(t *testing.T) {
	srv := restartServer("cold", 1)
	srv.Spec.Replicas = ptr.To(int32(0))
	r := newMCPServerReconciler(srv)

	reconcileMCPServer(t, r, "cold", "default")

	d, err := getDeployment(t, r, "cold")
	require.NoError(t, err)
	assert.Equal(t, int32(0), *d.Spec.Replicas)
	assert.Equal(t, mcpv1alpha2.MCPServerStateCold, getMCPServer(t, r, "cold", "default").Status.State)
}

// A same-named Deployment someone else controls is left alone and reported.
func TestMCPServer_ForeignDeployment_IsNotOverwritten(t *testing.T) {
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-provider-taken", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "nginx:1"}}},
			},
		},
	}
	r := newMCPServerReconciler(restartServer("taken", 1), foreign)

	reconcileMCPServer(t, r, "taken", "default")

	d, err := getDeployment(t, r, "taken")
	require.NoError(t, err)
	assert.Equal(t, "nginx:1", d.Spec.Template.Spec.Containers[0].Image, "not overwritten")
	assert.Equal(t, ReasonDeploymentNameTaken,
		getCondition(getMCPServer(t, r, "taken", "default").Status.Conditions, ConditionReady).Reason)
}

// ADR-031 T29: the status names the digests of the init-data the pod carries.
func TestMCPServer_ContainerMode_PublishesTheInitDataDigests(t *testing.T) {
	srv := restartServer("attested", 1)
	srv.Spec.InitData = "[data]\n\"key_ref\" = \"payments-tls\"\n"
	r := newMCPServerReconciler(srv)

	reconcileMCPServer(t, r, "attested", "default")

	out := getMCPServer(t, r, "attested", "default")
	assert.Equal(t, provider.InitDataDigests(srv.Spec.InitData), out.Status.InitDataDigests)
	d, err := getDeployment(t, r, "attested")
	require.NoError(t, err)
	assert.Contains(t, d.Spec.Template.Annotations, provider.AnnotationKataInitData)
}

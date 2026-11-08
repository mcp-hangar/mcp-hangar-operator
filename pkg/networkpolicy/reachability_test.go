package networkpolicy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Only a control that goes through next to a denied dial that does not is a
// block; a failed control proves nothing either way.
func TestVerdict_NeedsTheControl(t *testing.T) {
	cases := []struct {
		control, denied bool
		want            EnforcementVerdict
	}{
		{true, false, EnforcementObserved},
		{true, true, EnforcementNotObserved},
		{false, false, EnforcementUnknown},
		{false, true, EnforcementUnknown},
	}
	for _, tc := range cases {
		got, source := Verdict(tc.control, tc.denied)
		assert.Equal(t, tc.want, got, "control=%v denied=%v", tc.control, tc.denied)
		assert.NotEmpty(t, source)
	}
}

func operatorPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-abc", Namespace: "mcp-hangar", UID: "uid-1"},
		Spec: corev1.PodSpec{
			Containers:       []corev1.Container{{Name: "manager", Image: "registry.test/operator@sha256:abc"}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}},
		},
	}
}

func canaryPod(name, ip string, started time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "canary-" + name, Namespace: "mcp-hangar",
			Labels: map[string]string{LabelEnforcementProbe: "canary"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip, StartTime: &metav1.Time{Time: started}},
	}
}

func fakeCluster(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// replying answers the probe's question as a fenced canary would, and records
// the address it was dialled at and the line it was sent.
func replying(reply string, dialled, asked *string) func(context.Context, string) (net.Conn, error) {
	return func(_ context.Context, addr string) (net.Conn, error) {
		*dialled = addr
		ours, theirs := net.Pipe()
		go func() {
			defer func() { _ = theirs.Close() }()
			line, _ := bufio.NewReader(theirs).ReadString('\n')
			*asked = line
			_, _ = fmt.Fprint(theirs, reply)
		}()
		return ours, nil
	}
}

func TestRound_ObservedBlock(t *testing.T) {
	old := time.Now().Add(-time.Minute)
	c := fakeCluster(t, operatorPod(), canaryPod("a", "10.0.0.1", old), canaryPod("b", "10.0.0.2", old))
	var dialled, asked string
	p := &ReachabilityProbe{Client: c, Reader: c, Namespace: "mcp-hangar", PodName: "operator-abc",
		Dial: replying("control=open denied=closed\n", &dialled, &asked)}

	verdict, source := p.Round(context.Background())

	assert.Equal(t, EnforcementObserved, verdict, source)
	assert.Equal(t, "10.0.0.1:6443", dialled, "one canary is asked")
	assert.Equal(t, "dial 10.0.0.2\n", asked, "to dial the other")
}

func TestRound_DeniedPortReachable_NotObserved(t *testing.T) {
	old := time.Now().Add(-time.Minute)
	c := fakeCluster(t, operatorPod(), canaryPod("a", "10.0.0.1", old), canaryPod("b", "10.0.0.2", old))
	var dialled, asked string
	p := &ReachabilityProbe{Client: c, Reader: c, Namespace: "mcp-hangar", PodName: "operator-abc",
		Dial: replying("control=open denied=open\n", &dialled, &asked)}

	verdict, _ := p.Round(context.Background())

	assert.Equal(t, EnforcementNotObserved, verdict)
}

// A canary that only just started may not be programmed yet: doubt, not an
// alarm.
func TestRound_FreshCanary_Unknown(t *testing.T) {
	now := time.Now()
	c := fakeCluster(t, operatorPod(), canaryPod("a", "10.0.0.1", now), canaryPod("b", "10.0.0.2", now))
	var dialled, asked string
	p := &ReachabilityProbe{Client: c, Reader: c, Namespace: "mcp-hangar", PodName: "operator-abc",
		Dial: replying("control=open denied=open\n", &dialled, &asked)}

	verdict, source := p.Round(context.Background())

	assert.Equal(t, EnforcementUnknown, verdict)
	assert.Contains(t, source, "waiting")
	assert.Empty(t, dialled, "an unprogrammed fence would read as unenforced")
}

// The first round creates the canaries and their fence and answers doubt.
func TestRound_CreatesTheCanaries(t *testing.T) {
	c := fakeCluster(t, operatorPod())
	p := &ReachabilityProbe{Client: c, Reader: c, Namespace: "mcp-hangar", PodName: "operator-abc"}

	verdict, source := p.Round(context.Background())
	assert.Equal(t, EnforcementUnknown, verdict)
	assert.Contains(t, source, "waiting")

	var d appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "mcp-hangar", Name: canaryName}, &d))
	assert.Equal(t, int32(2), *d.Spec.Replicas)
	spec := d.Spec.Template.Spec
	assert.Equal(t, "registry.test/operator@sha256:abc", spec.Containers[0].Image, "the operator's own image")
	assert.Equal(t, []string{CanaryCommand}, spec.Containers[0].Args)
	assert.Equal(t, "pull", spec.ImagePullSecrets[0].Name)
	assert.False(t, *spec.AutomountServiceAccountToken)
	assert.Equal(t, "Pod/operator-abc", types(d.OwnerReferences), "goes with the operator's pod")
	var np networkingv1.NetworkPolicy
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "mcp-hangar", Name: canaryName}, &np))
	assert.Equal(t, map[string]string{LabelEnforcementProbe: "canary"}, np.Spec.PodSelector.MatchLabels,
		"every canary is fenced, so the one that dials on request reaches only the other")
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)
	require.Len(t, np.Spec.Egress, 1)
	assert.Equal(t, int32(CanaryControlPort), np.Spec.Egress[0].Ports[0].Port.IntVal)
}

func types(refs []metav1.OwnerReference) string {
	if len(refs) != 1 {
		return fmt.Sprintf("%d owners", len(refs))
	}
	return refs[0].Kind + "/" + refs[0].Name
}

func TestRound_NoOwnPod_Unknown(t *testing.T) {
	p := &ReachabilityProbe{}

	verdict, _ := p.Round(context.Background())

	assert.Equal(t, EnforcementUnknown, verdict)
}

// Before its first round the probe makes no claim.
func TestResult_BeforeARound_Unknown(t *testing.T) {
	verdict, _ := (&ReachabilityProbe{}).Result()

	assert.Equal(t, EnforcementUnknown, verdict)
}

// A change is announced once, so the policies re-read it; a repeat is not.
func TestRecord_AnnouncesChangesOnly(t *testing.T) {
	updates := make(chan event.GenericEvent, 1)
	p := &ReachabilityProbe{Updates: updates}

	p.record(EnforcementObserved, "blocked")
	require.Len(t, updates, 1)
	<-updates
	p.record(EnforcementObserved, "blocked")
	assert.Empty(t, updates)
	p.record(EnforcementNotObserved, "went through")
	assert.Len(t, updates, 1)
}

// The real canary and the real question over loopback, where nothing fences
// anything: both ports answer, so the verdict is NotObserved.
func TestCanary_OverLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, port := range []int{CanaryControlPort, CanaryDeniedPort} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Skipf("port %d is taken here: %v", port, err)
		}
		_ = ln.Close()
	}
	go func() { _ = RunCanary(ctx) }()
	p := &ReachabilityProbe{}
	var control, denied bool
	var err error
	for range 50 {
		if control, denied, err = p.ask(ctx, "127.0.0.1", "127.0.0.1"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	assert.True(t, control)
	assert.True(t, denied)
}

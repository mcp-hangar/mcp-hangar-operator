package webhook_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/internal/webhook"
)

func podRegScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, mcpv1alpha2.AddToScheme(s))
	return s
}

func podRequest(t *testing.T, pod *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Namespace: pod.Namespace,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func providerPod(name, namespace, provider string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if provider != "" {
		p.Labels = map[string]string{"mcp-hangar.io/provider": provider}
	}
	return p
}

func newValidator(t *testing.T, objs ...client.Object) *webhook.PodRegistrationValidator {
	t.Helper()
	s := podRegScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &webhook.PodRegistrationValidator{Client: c, Decoder: admission.NewDecoder(s)}
}

func TestPodRegistration_NoProviderLabel_Allowed(t *testing.T) {
	v := newValidator(t)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("plain", "demo", "")))
	assert.True(t, resp.Allowed)
}

func TestPodRegistration_UnregisteredProvider_Denied(t *testing.T) {
	v := newValidator(t) // no MCPServer in the fake client
	resp := v.Handle(context.Background(), podRequest(t, providerPod("ghost", "demo", "shadow")))
	require.False(t, resp.Allowed, "a provider pod with no MCPServer must be denied")
	assert.Contains(t, resp.Result.Message, "no registered MCPServer")
}

func TestPodRegistration_RegisteredProvider_Allowed(t *testing.T) {
	server := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "demo"}}
	v := newValidator(t, server)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("worker", "demo", "known")))
	assert.True(t, resp.Allowed, "a provider pod backed by an MCPServer is admitted")
}

func TestPodRegistration_RegisteredInOtherNamespace_Denied(t *testing.T) {
	// MCPServer "known" exists but in a different namespace -> still denied.
	server := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "other"}}
	v := newValidator(t, server)
	resp := v.Handle(context.Background(), podRequest(t, providerPod("worker", "demo", "known")))
	require.False(t, resp.Allowed, "registration is per-namespace")
	assert.Contains(t, resp.Result.Message, "no registered MCPServer")
}

// ---------------------------------------------------------------------------
// UPDATE (#189): the provider label is immutable once the pod is admitted,
// except that it may be removed. The per-server allow NetworkPolicy selects
// pods by this label, so a pod labelled after admission would otherwise
// inherit a registered server's egress with no admission call at all.
// ---------------------------------------------------------------------------

func podUpdateRequest(t *testing.T, oldPod, newPod *corev1.Pod) admission.Request {
	t.Helper()
	oldRaw, err := json.Marshal(oldPod)
	require.NoError(t, err)
	newRaw, err := json.Marshal(newPod)
	require.NoError(t, err)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Update,
		Namespace: newPod.Namespace,
		Object:    runtime.RawExtension{Raw: newRaw},
		OldObject: runtime.RawExtension{Raw: oldRaw},
	}}
}

func TestPodRegistration_Update_AddLabelUnregistered_Denied(t *testing.T) {
	v := newValidator(t) // no MCPServer in the fake client
	resp := v.Handle(context.Background(), podUpdateRequest(t,
		providerPod("plain", "demo", ""), providerPod("plain", "demo", "shadow")))
	require.False(t, resp.Allowed, "labelling an admitted pod with an unregistered provider must be denied")
	assert.Contains(t, resp.Result.Message, "after admission")
}

func TestPodRegistration_Update_AddLabelRegistered_Denied(t *testing.T) {
	// The acceptance criterion of #189: `kubectl label pod <unlabelled>
	// mcp-hangar.io/provider=<registered>` is denied even though the server
	// exists. On origin/main this was allowed -- the UPDATE never reached the
	// webhook, and had it, the CREATE path only checks the new label.
	server := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "demo"}}
	v := newValidator(t, server)
	resp := v.Handle(context.Background(), podUpdateRequest(t,
		providerPod("plain", "demo", ""), providerPod("plain", "demo", "known")))
	require.False(t, resp.Allowed, "a pod admitted unlabelled must not join a registered server's allow-policy later")
	assert.Contains(t, resp.Result.Message, "after admission")
}

func TestPodRegistration_Update_ChangeLabelBetweenRegistered_Denied(t *testing.T) {
	// Both servers exist; the label is still immutable. The controller never
	// relabels the pods it creates, so a relabel is never a legitimate write.
	a := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "demo"}}
	b := &mcpv1alpha2.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "beta", Namespace: "demo"}}
	v := newValidator(t, a, b)
	resp := v.Handle(context.Background(), podUpdateRequest(t,
		providerPod("worker", "demo", "alpha"), providerPod("worker", "demo", "beta")))
	require.False(t, resp.Allowed, "changing the provider label must be denied even between registered servers")
	assert.Contains(t, resp.Result.Message, "immutable once set")
}

func TestPodRegistration_Update_RemoveLabel_Allowed(t *testing.T) {
	v := newValidator(t)
	resp := v.Handle(context.Background(), podUpdateRequest(t,
		providerPod("worker", "demo", "known"), providerPod("worker", "demo", "")))
	assert.True(t, resp.Allowed, "removing the label takes the pod out of the allow-policy; safe direction")
}

func TestPodRegistration_Update_UnrelatedChangeOnLabelledPod_Allowed(t *testing.T) {
	// No MCPServer in the fake client: the server may have been deleted since
	// the pod was admitted. A finalizer/ownerRef/annotation patch that leaves
	// the label alone must still go through, or the pod can never be cleaned up.
	oldPod := providerPod("worker", "demo", "known")
	newPod := providerPod("worker", "demo", "known")
	newPod.Finalizers = []string{"example.com/drain"}
	newPod.Annotations = map[string]string{"example.com/note": "x"}
	v := newValidator(t)
	resp := v.Handle(context.Background(), podUpdateRequest(t, oldPod, newPod))
	assert.True(t, resp.Allowed, "an update that leaves the provider label unchanged is not gated")
}

func TestPodRegistration_Update_UnlabelledPodStaysUnlabelled_Allowed(t *testing.T) {
	oldPod := providerPod("plain", "demo", "")
	newPod := providerPod("plain", "demo", "")
	newPod.Labels = map[string]string{"app": "other"}
	v := newValidator(t)
	resp := v.Handle(context.Background(), podUpdateRequest(t, oldPod, newPod))
	assert.True(t, resp.Allowed)
}

func TestPodRegistration_Update_StatusChangeOnLabelledPod_Allowed(t *testing.T) {
	// Belt and braces: a kubelet status write goes through pods/status, which
	// `resources: pods` does not match (asserted on the manifest below), but if
	// a status change ever did arrive on the main resource it is still allowed
	// because the label is unchanged.
	oldPod := providerPod("worker", "demo", "known")
	newPod := providerPod("worker", "demo", "known")
	newPod.Status.Phase = corev1.PodRunning
	v := newValidator(t)
	resp := v.Handle(context.Background(), podUpdateRequest(t, oldPod, newPod))
	assert.True(t, resp.Allowed)
}

// TestPodRegistration_Manifest_RoutesCreateAndUpdate pins what the cluster
// routes to the webhook: both CREATE and UPDATE on the main pods resource and
// nothing else -- in particular no pods/status, so a kubelet status patch on a
// labelled pod can never be denied by a failurePolicy: Fail webhook.
func TestPodRegistration_Manifest_RoutesCreateAndUpdate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "webhook", "manifests.yaml"))
	require.NoError(t, err)
	var cfg struct {
		Webhooks []struct {
			Name  string `json:"name"`
			Rules []struct {
				Operations []string `json:"operations"`
				Resources  []string `json:"resources"`
			} `json:"rules"`
		} `json:"webhooks"`
	}
	require.NoError(t, yaml.Unmarshal(data, &cfg))

	found := false
	for _, w := range cfg.Webhooks {
		if w.Name != "vpod-registration.kb.io" {
			continue
		}
		found = true
		require.Len(t, w.Rules, 1)
		assert.ElementsMatch(t, []string{"CREATE", "UPDATE"}, w.Rules[0].Operations,
			"a label change is an UPDATE; a CREATE-only rule is the #189 bypass")
		assert.Equal(t, []string{"pods"}, w.Rules[0].Resources,
			"main resource only: pods/status must never be routed to a fail-closed webhook")
	}
	require.True(t, found, "vpod-registration.kb.io missing from config/webhook/manifests.yaml")
}
